/*
 *  Copyright 2024 Keyfactor
 *  Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
 *  Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions
 *  and limitations under the License.
 */

package kfbackend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/helper/consts"
	"github.com/hashicorp/vault/sdk/helper/errutil"
	"github.com/hashicorp/vault/sdk/logical"
)

const kf_revoke_path = "/Certificates/Revoke"

type revocationInfo struct {
	CertificateBytes  []byte    `json:"certificate_bytes"`
	RevocationTime    int64     `json:"revocation_time"`
	RevocationTimeUTC time.Time `json:"revocation_time_utc"`
}

func pathCerts(b *keyfactorBackend) []*framework.Path {

	return []*framework.Path{
		{ // certs list
			Pattern: "certs/?$",

			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ListOperation: b.pathFetchCertList,
			},

			HelpSynopsis:    pathFetchListHelpSyn,
			HelpDescription: pathFetchListHelpDesc,
		},
		{ // issue
			Pattern: "issue/" + framework.GenericNameRegex("role"),

			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.UpdateOperation: b.pathIssue,
			},

			HelpSynopsis:    pathIssueHelpSyn,
			HelpDescription: pathIssueHelpDesc,
			Fields:          addNonCACommonFields(map[string]*framework.FieldSchema{}),
		},
		{ // sign
			Pattern: "sign/" + framework.GenericNameRegex("role"),

			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.UpdateOperation: b.pathSign,
			},

			HelpSynopsis:    pathSignHelpSyn,
			HelpDescription: pathSignHelpDesc,
			Fields: addNonCACommonFields(map[string]*framework.FieldSchema{
				"csr": {
					Type:        framework.TypeString,
					Default:     "",
					Description: `PEM-format CSR to be signed.`,
					Required:    true,
				}}),
		},
		{ // fetch cert
			Pattern: `certs/(?P<serial>[0-9A-Fa-f-:]+)`,
			Fields: map[string]*framework.FieldSchema{
				"serial": {
					Type: framework.TypeString,
					Description: `Certificate serial number, in colon- or
		hyphen-separated octal`,
				},
			},

			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ReadOperation: b.pathFetchCert,
			},

			HelpSynopsis:    pathFetchHelpSyn,
			HelpDescription: pathFetchHelpDesc,
		},
		{ // revoke
			Pattern: `revoke/?$`,

			Fields: map[string]*framework.FieldSchema{
				"serial": {
					Type:        framework.TypeString,
					Description: `The cerial number of the certificate to revoke`,
				},
			},
			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.UpdateOperation: b.pathRevokeCert,
				logical.CreateOperation: b.pathRevokeCert,
			},

			HelpSynopsis:    pathRevokeHelpSyn,
			HelpDescription: pathRevokeHelpDesc,
		},
	}
}

func (b *keyfactorBackend) pathFetchCertList(ctx context.Context, req *logical.Request, data *framework.FieldData) (response *logical.Response, retErr error) {
	entries, err := req.Storage.List(ctx, "certs/")
	if err != nil {
		return nil, err
	}

	return logical.ListResponse(entries), nil
}

func (b *keyfactorBackend) pathFetchCert(ctx context.Context, req *logical.Request, data *framework.FieldData) (response *logical.Response, retErr error) {
	var serial, contentType string
	var certEntry, revokedEntry *logical.StorageEntry
	var funcErr error
	var certificate string
	var revocationTime int64
	response = &logical.Response{
		Data: map[string]interface{}{},
	}

	// Some of these need to return raw and some non-raw;
	// this is basically handled by setting contentType or not.
	// Errors don't cause an immediate exit, because the raw
	// paths still need to return raw output.

	b.Logger().Debug("fetching cert, path = " + req.Path)

	serial = data.Get("serial").(string)

	if len(serial) == 0 {
		response = logical.ErrorResponse("The serial number must be provided")
		goto reply
	}

	b.Logger().Debug("fetching certificate; serial = " + serial)

	certEntry, funcErr = fetchCertBySerial(ctx, req, req.Path, serial)
	if funcErr != nil {
		switch funcErr.(type) {
		case errutil.UserError:
			response = logical.ErrorResponse(funcErr.Error())
			goto reply
		case errutil.InternalError:
			retErr = funcErr
			goto reply
		}
	}
	if certEntry == nil {
		response = nil
		goto reply
	}

	b.Logger().Debug("fetched certEntry.Value = ", certEntry.Value)

	certificate = string(certEntry.Value)
	revokedEntry, funcErr = fetchCertBySerial(ctx, req, "revoked/", serial)
	if funcErr != nil {
		switch funcErr.(type) {
		case errutil.UserError:
			response = logical.ErrorResponse(funcErr.Error())
			goto reply
		case errutil.InternalError:
			retErr = funcErr
			goto reply
		}
	}
	if revokedEntry != nil {
		var revInfo revocationInfo
		err := revokedEntry.DecodeJSON(&revInfo)
		if err != nil {
			return logical.ErrorResponse(fmt.Sprintf("Error decoding revocation entry for serial %s: %s", serial, err)), nil
		}
		revocationTime = revInfo.RevocationTime
	}

reply:
	switch {
	case len(contentType) != 0:
		response = &logical.Response{
			Data: map[string]interface{}{
				logical.HTTPContentType: contentType,
				logical.HTTPRawBody:     certificate,
			}}
		if retErr != nil {
			if b.Logger().IsWarn() {
				b.Logger().Warn("possible error, but cannot return in raw response. Note that an empty CA probably means none was configured, and an empty CRL is possibly correct", "error", retErr)
			}
		}
		retErr = nil
		if len(certificate) > 0 {
			response.Data[logical.HTTPStatusCode] = 200
		} else {
			response.Data[logical.HTTPStatusCode] = 204
		}
	case retErr != nil:
		response = nil
		return
	case response == nil:
		return
	case response.IsError():
		return response, nil
	default:
		response.Data["certificate"] = string(certificate)
		response.Data["revocation_time"] = revocationTime
	}

	return
}

// pathIssue issues a certificate and private key from given parameters,
// subject to role restrictions
func (b *keyfactorBackend) pathIssue(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	roleName := data.Get("role").(string)
	b.Logger().Debug(fmt.Sprintf("got role name of %s", roleName))

	// Get the role
	role, err := b.getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return logical.ErrorResponse(fmt.Sprintf("unknown role: %s", roleName)), nil
	}

	if role.KeyType == "any" {
		return logical.ErrorResponse("role key type \"any\" not allowed for issuing certificates, only signing"), nil
	}

	return b.pathIssueSignCert(ctx, req, data, role)
}

// pathSign issues a certificate from a submitted CSR, subject to role
// restrictions
func (b *keyfactorBackend) pathSign(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	roleName := data.Get("role").(string)
	csr := data.Get("csr").(string)
	// Get the role
	role, err := b.getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return logical.ErrorResponse(fmt.Sprintf("unknown role: %s", roleName)), nil
	}

	caName := data.Get("ca").(string)
	templateName := data.Get("template").(string)

	b.Logger().Debug("CA Name parameter = " + caName)
	b.Logger().Debug("Template name parameter = " + templateName)

	metadata := data.Get("metadata").(string)

	if metadata == "" {
		metadata = "{}"
	}

	// verify that any passed metadata string is valid JSON

	if !b.isValidJSON(metadata) {
		err := fmt.Errorf("'%s' is not a valid JSON string", metadata)
		b.Logger().Error(err.Error())
		return nil, err
	}

	certs, serial, errr := b.submitCSR(ctx, req, csr, caName, templateName, metadata)

	if errr != nil {
		return nil, fmt.Errorf("could not sign csr: %s", errr)
	}
	response := &logical.Response{
		Data: map[string]interface{}{
			"certificate":   certs[0],
			"issuing_ca":    certs[1],
			"serial_number": serial,
		},
	}

	return response, nil
}

func (b *keyfactorBackend) pathIssueSignCert(ctx context.Context, req *logical.Request, data *framework.FieldData, role *roleEntry) (*logical.Response, error) {
	// If storing the certificate and on a performance standby, forward this request on to the primary
	if !role.NoStore && b.System().ReplicationState().HasState(consts.ReplicationPerformanceStandby) {
		return nil, logical.ErrReadOnly
	}

	var ip_sans []string
	var dns_sans []string

	arg, _ := json.Marshal(req.Data)
	b.Logger().Debug(string(arg))

	// get common name
	b.Logger().Debug("parsing common_name...")
	cn, ok := data.GetOk("common_name")

	if !ok {
		return nil, fmt.Errorf("common_name must be provided to issue certificate")
	}

	cn = cn.(string)

	if cn == "" {
		return nil, fmt.Errorf("common_name must be provided to issue certificate")
	}

	b.Logger().Debug(fmt.Sprintf("common_name = %s", cn))

	// get dns sans (required)
	b.Logger().Debug("parsing dns_sans...")
	dns_sans_string, ok := data.GetOk("dns_sans")

	if !ok {
		return nil, fmt.Errorf("dns_sans must be provided to issue certificate")
	}

	dns_sans_string = dns_sans_string.(string)

	if dns_sans_string == "" {
		return nil, fmt.Errorf("dns_sans must be provided to issue certificate")
	}

	dns_sans = strings.Split(dns_sans_string.(string), ",")

	if len(dns_sans) == 0 {
		return nil, fmt.Errorf("dns_sans must be provided to issue certificate")
	}

	b.Logger().Debug(fmt.Sprintf("dns_sans = %s", dns_sans))

	// get ip sans (optional)
	b.Logger().Debug("parsing ip_sans...")
	ip_sans_string, ok := data.GetOk("ip_sans")
	if ok && ip_sans_string != "" {
		ip_sans = strings.Split(ip_sans_string.(string), ",")
	}

	// get the CA name
	b.Logger().Debug("parsing ca...")
	caName := data.Get("ca").(string)
	if caName == "" {
		b.Logger().Debug("no ca passed, retreiving from config")
		caName = b.cachedConfig.CertAuthority
	}
	b.Logger().Debug(fmt.Sprintf("ca name = %s", caName))

	// get the template name
	b.Logger().Debug("parsing template name...")
	templateName := data.Get("template").(string)
	if templateName == "" {
		b.Logger().Debug("no template name in parameters, retrieving from config")
		templateName = b.cachedConfig.CertTemplate
	}
	b.Logger().Debug(fmt.Sprintf("template name: %s", templateName))

	//check role permissions
	var err_resp error
	var valid bool
	var hasSuffix bool

	// check the allowed domains for a match.
	// if allowed_domains is '*', allow any domain

	for _, v := range role.AllowedDomains {
		if v == "*" || strings.HasSuffix(cn.(string), v) { // if it has the suffix..
			hasSuffix = true
			if cn.(string) == v || role.AllowSubdomains { // and there is an exact match, or subdomains are allowed..
				valid = true // then it is valid
			}
		}
	}

	if !valid {
		err_resp = fmt.Errorf("common name not allowed for role")
	}
	if !valid && hasSuffix {
		err_resp = fmt.Errorf("sub-domains not allowed for role")
	}

	if err_resp != nil {
		return nil, err_resp
	}

	// check the provided DNS sans against allowed domains
	var cnMatch = false
	b.Logger().Trace("checking dns sans" + dns_sans[0] + ", ...")
	for u := range dns_sans {
		valid = false
		hasSuffix = false
		cnMatch = cnMatch || dns_sans[u] == cn.(string) // check to make sure at least one of the dns_sans match the cn
		b.Logger().Trace("checking SANs")
		for _, v := range role.AllowedDomains {
			if v == "*" || strings.HasSuffix(dns_sans[u], v) { // if it has the suffix..
				hasSuffix = true
				if dns_sans[u] == v || role.AllowSubdomains { // and there is an exact match, or subdomains are allowed..
					valid = true // then it is valid
				}
			}
		}
		if !valid {
			err_resp = fmt.Errorf("subject alternative name %s not allowed for provided role", dns_sans[u])
		}
		if !valid && hasSuffix {
			err_resp = fmt.Errorf("sub-domains not allowed for role")
		}
	}

	b.Logger().Trace("cnMatch = " + strconv.FormatBool(cnMatch))

	if !cnMatch {
		err_resp = fmt.Errorf("at least one DNS SAN is required to match the supplied Common Name for RFC 2818 compliance")
	}

	metadata := data.Get("metadata").(string)

	if metadata == "" {
		metadata = "{}"
	}

	// verify that any passed metadata string is valid JSON

	if !b.isValidJSON(metadata) {
		err_resp := fmt.Errorf("'%s' is not a valid JSON string", metadata)
		b.Logger().Error(err_resp.Error())
	}

	if err_resp != nil {
		return nil, err_resp
	}

	//generate and submit CSR
	b.Logger().Debug("generating the CSR...")
	csr, key := b.generateCSR(cn.(string), ip_sans, dns_sans)
	certs, serial, errr := b.submitCSR(ctx, req, csr, caName, templateName, metadata)

	if errr != nil {
		return nil, fmt.Errorf("could not enroll certificate: %s", errr)
	}

	// Conform response to Vault PKI API
	response := &logical.Response{
		Data: map[string]interface{}{
			"certificate":      certs[0],
			"issuing_ca":       certs[1],
			"private_key":      "-----BEGIN RSA PRIVATE KEY-----\n" + base64.StdEncoding.EncodeToString(key) + "\n-----END RSA PRIVATE KEY-----",
			"private_key_type": "rsa",
			"revocation_time":  0,
			"serial_number":    serial,
		},
	}

	return response, nil
}

func (b *keyfactorBackend) pathRevokeCert(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	serial := data.Get("serial").(string)
	b.Logger().Debug("serial = " + serial)

	if len(serial) == 0 {
		return logical.ErrorResponse("The serial number must be provided"), nil
	}

	if b.System().ReplicationState().HasState(consts.ReplicationPerformanceStandby) {
		return nil, logical.ErrReadOnly
	}

	// We store and identify by lowercase colon-separated hex, but other
	// utilities use dashes and/or uppercase, so normalize
	serial = strings.Replace(strings.ToLower(serial), "-", ":", -1)

	return revokeCert(ctx, b, req, serial, false)
}

// Revokes a cert, and tries to be smart about error recovery
func revokeCert(ctx context.Context, b *keyfactorBackend, req *logical.Request, serial string, fromLease bool) (*logical.Response, error) {
	// As this backend is self-contained and this function does not hook into
	// third parties to manage users or resources, if the mount is tainted,
	// revocation doesn't matter anyways -- the CRL that would be written will
	// be immediately blown away by the view being cleared. So we can simply
	// fast path a successful exit.
	if b.System().Tainted() {
		return nil, nil
	}

	// get client
	client, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, fmt.Errorf("error getting client: %w", err)
	}

	b.Logger().Debug("Closing idle connections")
	client.httpClient.CloseIdleConnections()

	// config, err := b.fetchConfig(ctx, req.Storage)
	// if err != nil {
	// 	return nil, err
	// }
	// if config == nil {
	// 	return logical.ErrorResponse("could not load configuration"), nil
	// }

	kfId, err := req.Storage.Get(ctx, "kfId/"+serial) //retrieve the keyfactor certificate ID, keyed by sn here
	if err != nil {
		b.Logger().Error("Unable to retreive Keyfactor certificate ID for cert with serial: "+serial, err)
		return nil, err
	}

	var keyfactorId int
	err = kfId.DecodeJSON(&keyfactorId)

	if err != nil {
		b.Logger().Error("Unable to parse stored certificate ID for cert with serial: "+serial, err)
		return nil, err
	}

	// set up keyfactor api request
	url := b.cachedConfig.KeyfactorUrl + "/" + b.cachedConfig.CommandAPIPath + kf_revoke_path
	payload := fmt.Sprintf(`{
		"CertificateIds": [
		  %d
		],
		"Reason": 0,
		"Comment": "%s",
		"EffectiveDate": "%s"},
		"CollectionId": 0
	  }`, keyfactorId, "via HashiCorp Vault", time.Now().Format(time.RFC3339))
	b.Logger().Debug("Sending revocation request.  payload =  " + payload)
	httpReq, _ := http.NewRequest("POST", url, strings.NewReader(payload))

	httpReq.Header.Add("x-keyfactor-requested-with", "APIClient")
	httpReq.Header.Add("content-type", "application/json")

	res, err := client.httpClient.Do(httpReq)
	if err != nil {
		b.Logger().Error("Revoke failed: {{err}}", err)
		return nil, err
	}
	r, _ := io.ReadAll(res.Body)

	b.Logger().Debug("response received.  Status code " + fmt.Sprint(res.StatusCode) + " response body: \n " + string(r[:]))
	if res.StatusCode != 204 && res.StatusCode != 200 {
		b.Logger().Info("revocation failed: server returned" + fmt.Sprint(res.StatusCode))
		b.Logger().Info("error response = " + string(r[:]))
		return nil, fmt.Errorf("revocation failed: server returned  %s\n ", res.Status)
	}

	defer res.Body.Close()

	alreadyRevoked := false
	var revInfo revocationInfo

	revEntry, err := fetchCertBySerial(ctx, req, "revoked/", serial)
	if err != nil {
		switch err.(type) {
		case errutil.UserError:
			return logical.ErrorResponse(err.Error()), nil
		case errutil.InternalError:
			return nil, err
		}
	}
	if revEntry != nil {
		// Set the revocation info to the existing values
		alreadyRevoked = true
		err = revEntry.DecodeJSON(&revInfo)
		if err != nil {
			return nil, fmt.Errorf("error decoding existing revocation info")
		}
	}

	if !alreadyRevoked {
		certEntry, err := fetchCertBySerial(ctx, req, "certs/", serial)
		if err != nil {
			switch err.(type) {
			case errutil.UserError:
				return logical.ErrorResponse(err.Error()), nil
			case errutil.InternalError:
				return nil, err
			}
		}
		if certEntry == nil {
			if fromLease {
				// We can't write to revoked/ or update the CRL anyway because we don't have the cert,
				// and there's no reason to expect this will work on a subsequent
				// retry.  Just give up and let the lease get deleted.
				b.Logger().Warn("expired certificate revoke failed because not found in storage, treating as success", "serial", serial)
				return nil, nil
			}
			return logical.ErrorResponse(fmt.Sprintf("certificate with serial %s not found", serial)), nil
		}
		b.Logger().Debug("certEntry key = " + certEntry.Key)
		b.Logger().Debug("certEntry value = " + string(certEntry.Value))

		currTime := time.Now()
		revInfo.CertificateBytes = certEntry.Value
		revInfo.RevocationTime = currTime.Unix()
		revInfo.RevocationTimeUTC = currTime.UTC()

		revEntry, err = logical.StorageEntryJSON("revoked/"+normalizeSerial(serial), revInfo)
		if err != nil {
			return nil, fmt.Errorf("error creating revocation entry")
		}

		err = req.Storage.Put(ctx, revEntry)
		if err != nil {
			return nil, fmt.Errorf("error saving revoked certificate to new location")
		}
	}

	resp := &logical.Response{
		Data: map[string]interface{}{
			"revocation_time": revInfo.RevocationTime,
		},
	}
	if !revInfo.RevocationTimeUTC.IsZero() {
		resp.Data["revocation_time_rfc3339"] = revInfo.RevocationTimeUTC.Format(time.RFC3339)
	}
	return resp, nil
}

const pathIssueHelpSyn = `
Request a certificate using a certain role with the provided details.
example: vault write keyfactor/issue/<role> common_name=<cn> dns_sans=<dns sans>
`

const pathIssueHelpDesc = `
This path allows requesting a certificate to be issued according to the
policy of the given role. The certificate will only be issued if the
requested details are allowed by the role policy.

This path returns a certificate and a private key. If you want a workflow
that does not expose a private key, generate a CSR locally and use the
sign path instead.
`

const pathSignHelpSyn = `
Request certificates using a certain role with the provided details.
example: vault write keyfactor/sign/<role> csr=<csr>
`

const pathSignHelpDesc = `
This path allows requesting certificates to be issued according to the
policy of the given role. The certificate will only be issued if the
requested common name is allowed by the role policy.

This path requires a CSR; if you want Vault to generate a private key
for you, use the issue path instead.

Note: the CSR must contain at least one DNS SANs entry.
`

const pathFetchHelpSyn = `
Fetch a CA, CRL, CA Chain, or non-revoked certificate.
`

const pathFetchHelpDesc = `
This allows certificates to be fetched. If using the fetch/ prefix any non-revoked certificate can be fetched.
Using "ca" or "crl" as the value fetches the appropriate information in DER encoding. Add "/pem" to either to get PEM encoding.
Using "ca_chain" as the value fetches the certificate authority trust chain in PEM encoding.
`

const pathFetchListHelpSyn = `
List all of the certificates managed by this secrets engine.
`

const pathFetchListHelpDesc = `
Use with the "list" command to display the list of certificate serial numbers for certificates managed by this secrets engine.
`

const pathRevokeHelpSyn = `
Revoke a certificate by serial number.
`

const pathRevokeHelpDesc = `
This allows certificates to be revoked using its serial number. A root token is required.
`
