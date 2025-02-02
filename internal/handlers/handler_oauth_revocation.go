package handlers

import (
	"net/http"

	"github.com/ory/fosite"

	"github.com/authelia/authelia/v4/internal/middlewares"
)

// OAuthRevocationPOST handles POST requests to the OAuth 2.0 Revocation endpoint.
//
// https://datatracker.ietf.org/doc/html/rfc7009
func OAuthRevocationPOST(ctx *middlewares.AutheliaCtx, rw http.ResponseWriter, req *http.Request) {
	var err error

	if err = ctx.Providers.OpenIDConnect.Fosite.NewRevocationRequest(ctx, req); err != nil {
		rfc := fosite.ErrorToRFC6749Error(err)

		ctx.Logger.Errorf("Revocation Request failed with error: %s", rfc.GetDescription())
	}

	ctx.Providers.OpenIDConnect.Fosite.WriteRevocationResponse(rw, err)
}
