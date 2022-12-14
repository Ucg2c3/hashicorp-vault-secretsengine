package keyfactor

// import (
// 	"context"
// 	"strings"

// 	"github.com/hashicorp/vault/sdk/framework"
// 	"github.com/hashicorp/vault/sdk/helper/consts"
// 	"github.com/hashicorp/vault/sdk/logical"
// )

// func pathRevoke(b *keyfactorBackend) *framework.Path {
// 	return &framework.Path{
// 		Pattern: `revoke/?$`,

// 		Fields: map[string]*framework.FieldSchema{
// 			"serial": {
// 				Type:        framework.TypeString,
// 				Description: `The cerial number of the certificate to revoke`,
// 			},
// 		},
// 		Callbacks: map[logical.Operation]framework.OperationFunc{
// 			logical.UpdateOperation: b.pathRevokeWrite,
// 			logical.CreateOperation: b.pathRevokeWrite,
// 		},

// 		HelpSynopsis:    pathRevokeHelpSyn,
// 		HelpDescription: pathRevokeHelpDesc,
// 	}
// }

// func (b *keyfactorBackend) pathRevokeWrite(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
// 	//path := data.Get("path").(string)
// 	//b.Logger().Debug("path = " + path)

// 	serial := data.Get("serial").(string)
// 	b.Logger().Debug("serial = " + serial)

// 	if len(serial) == 0 {
// 		return logical.ErrorResponse("The serial number must be provided"), nil
// 	}

// 	if b.System().ReplicationState().HasState(consts.ReplicationPerformanceStandby) {
// 		return nil, logical.ErrReadOnly
// 	}

// 	// We store and identify by lowercase colon-separated hex, but other
// 	// utilities use dashes and/or uppercase, so normalize
// 	serial = strings.Replace(strings.ToLower(serial), "-", ":", -1)

// 	return revokeCert(ctx, b, req, serial, false)
// }

// const pathRevokeHelpSyn = `
// Revoke a certificate by serial number.
// `

// const pathRevokeHelpDesc = `
// This allows certificates to be revoked using its serial number. A root token is required.
// `
