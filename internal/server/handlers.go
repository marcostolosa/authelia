package server

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	duoapi "github.com/duosecurity/duo_api_golang"
	"github.com/fasthttp/router"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/expvarhandler"
	"github.com/valyala/fasthttp/pprofhandler"

	"github.com/authelia/authelia/v4/internal/configuration/schema"
	"github.com/authelia/authelia/v4/internal/duo"
	"github.com/authelia/authelia/v4/internal/handlers"
	"github.com/authelia/authelia/v4/internal/logging"
	"github.com/authelia/authelia/v4/internal/middlewares"
	"github.com/authelia/authelia/v4/internal/oidc"
	"github.com/authelia/authelia/v4/internal/utils"
)

// Replacement for the default error handler in fasthttp.
func handlerError() func(ctx *fasthttp.RequestCtx, err error) {
	logger := logging.Logger()

	headerXForwardedFor := []byte(fasthttp.HeaderXForwardedFor)

	getRemoteIP := func(ctx *fasthttp.RequestCtx) string {
		if hdr := ctx.Request.Header.PeekBytes(headerXForwardedFor); hdr != nil {
			ips := strings.Split(string(hdr), ",")

			if len(ips) > 0 {
				return strings.Trim(ips[0], " ")
			}
		}

		return ctx.RemoteIP().String()
	}

	return func(ctx *fasthttp.RequestCtx, err error) {
		switch e := err.(type) {
		case *fasthttp.ErrSmallBuffer:
			logger.Tracef("Request was too large to handle from client %s. Response Code %d.", getRemoteIP(ctx), fasthttp.StatusRequestHeaderFieldsTooLarge)
			ctx.Error("request header too large", fasthttp.StatusRequestHeaderFieldsTooLarge)
		case *net.OpError:
			if e.Timeout() {
				logger.Tracef("Request timeout occurred while handling from client %s: %s. Response Code %d.", getRemoteIP(ctx), ctx.RequestURI(), fasthttp.StatusRequestTimeout)
				ctx.Error("request timeout", fasthttp.StatusRequestTimeout)
			} else {
				logger.Tracef("An unknown error occurred while handling a request from client %s: %s. Response Code %d.", getRemoteIP(ctx), ctx.RequestURI(), fasthttp.StatusBadRequest)
				ctx.Error("error when parsing request", fasthttp.StatusBadRequest)
			}
		default:
			logger.Tracef("An unknown error occurred while handling a request from client %s: %s. Response Code %d.", getRemoteIP(ctx), ctx.RequestURI(), fasthttp.StatusBadRequest)
			ctx.Error("error when parsing request", fasthttp.StatusBadRequest)
		}
	}
}

func handlerNotFound(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		path := strings.ToLower(string(ctx.Path()))

		for i := 0; i < len(httpServerDirs); i++ {
			if path == httpServerDirs[i].name || strings.HasPrefix(path, httpServerDirs[i].prefix) {
				handlers.SetStatusCodeResponse(ctx, fasthttp.StatusNotFound)

				return
			}
		}

		next(ctx)
	}
}

func handlerMethodNotAllowed(ctx *fasthttp.RequestCtx) {
	handlers.SetStatusCodeResponse(ctx, fasthttp.StatusMethodNotAllowed)
}

func getHandler(config schema.Configuration, providers middlewares.Providers) fasthttp.RequestHandler {
	rememberMe := strconv.FormatBool(config.Session.RememberMeDuration != schema.RememberMeDisabled)
	resetPassword := strconv.FormatBool(!config.AuthenticationBackend.DisableResetPassword)

	resetPasswordCustomURL := config.AuthenticationBackend.PasswordReset.CustomURL.String()

	duoSelfEnrollment := f
	if config.DuoAPI != nil {
		duoSelfEnrollment = strconv.FormatBool(config.DuoAPI.EnableSelfEnrollment)
	}

	https := config.Server.TLS.Key != "" && config.Server.TLS.Certificate != ""

	serveIndexHandler := ServeTemplatedFile(embeddedAssets, indexFile, config.Server.AssetPath, duoSelfEnrollment, rememberMe, resetPassword, resetPasswordCustomURL, config.Session.Name, config.Theme, https)
	serveSwaggerHandler := ServeTemplatedFile(swaggerAssets, indexFile, config.Server.AssetPath, duoSelfEnrollment, rememberMe, resetPassword, resetPasswordCustomURL, config.Session.Name, config.Theme, https)
	serveSwaggerAPIHandler := ServeTemplatedFile(swaggerAssets, apiFile, config.Server.AssetPath, duoSelfEnrollment, rememberMe, resetPassword, resetPasswordCustomURL, config.Session.Name, config.Theme, https)

	handlerPublicHTML := newPublicHTMLEmbeddedHandler()
	handlerLocales := newLocalesEmbeddedHandler()

	middleware := middlewares.AutheliaMiddleware(config, providers)

	policyCORSPublicGET := middlewares.NewCORSPolicyBuilder().
		WithAllowedMethods("OPTIONS", "GET").
		WithAllowedOrigins("*").
		Build()

	r := router.New()

	// Static Assets.
	r.GET("/", middleware(serveIndexHandler))

	for _, f := range rootFiles {
		r.GET("/"+f, handlerPublicHTML)
	}

	r.GET("/favicon.ico", middlewares.AssetOverrideMiddleware(config.Server.AssetPath, 0, handlerPublicHTML))
	r.GET("/static/media/logo.png", middlewares.AssetOverrideMiddleware(config.Server.AssetPath, 2, handlerPublicHTML))
	r.GET("/static/{filepath:*}", handlerPublicHTML)

	// Locales.
	r.GET("/locales/{language:[a-z]{1,3}}-{variant:[a-zA-Z0-9-]+}/{namespace:[a-z]+}.json", middlewares.AssetOverrideMiddleware(config.Server.AssetPath, 0, handlerLocales))
	r.GET("/locales/{language:[a-z]{1,3}}/{namespace:[a-z]+}.json", middlewares.AssetOverrideMiddleware(config.Server.AssetPath, 0, handlerLocales))

	// Swagger.
	r.GET("/api/", middleware(serveSwaggerHandler))
	r.OPTIONS("/api/", policyCORSPublicGET.HandleOPTIONS)
	r.GET("/api/"+apiFile, policyCORSPublicGET.Middleware(middleware(serveSwaggerAPIHandler)))
	r.OPTIONS("/api/"+apiFile, policyCORSPublicGET.HandleOPTIONS)

	for _, file := range swaggerFiles {
		r.GET("/api/"+file, handlerPublicHTML)
	}

	r.GET("/api/health", middleware(handlers.HealthGET))
	r.GET("/api/state", middleware(handlers.StateGET))

	r.GET("/api/configuration", middleware(middlewares.Require1FA(handlers.ConfigurationGET)))

	r.GET("/api/configuration/password-policy", middleware(handlers.PasswordPolicyConfigurationGet))

	r.GET("/api/verify", middleware(handlers.VerifyGET(config.AuthenticationBackend)))
	r.HEAD("/api/verify", middleware(handlers.VerifyGET(config.AuthenticationBackend)))

	r.POST("/api/checks/safe-redirection", middleware(handlers.CheckSafeRedirectionPOST))

	delayFunc := middlewares.TimingAttackDelay(10, 250, 85, time.Second)

	r.POST("/api/firstfactor", middleware(handlers.FirstFactorPOST(delayFunc)))
	r.POST("/api/logout", middleware(handlers.LogoutPOST))

	// Only register endpoints if forgot password is not disabled.
	if !config.AuthenticationBackend.DisableResetPassword &&
		config.AuthenticationBackend.PasswordReset.CustomURL.String() == "" {
		// Password reset related endpoints.
		r.POST("/api/reset-password/identity/start", middleware(handlers.ResetPasswordIdentityStart))
		r.POST("/api/reset-password/identity/finish", middleware(handlers.ResetPasswordIdentityFinish))
		r.POST("/api/reset-password", middleware(handlers.ResetPasswordPOST))
	}

	// Information about the user.
	r.GET("/api/user/info", middleware(middlewares.Require1FA(handlers.UserInfoGET)))
	r.POST("/api/user/info", middleware(middlewares.Require1FA(handlers.UserInfoPOST)))
	r.POST("/api/user/info/2fa_method", middleware(middlewares.Require1FA(handlers.MethodPreferencePOST)))

	if !config.TOTP.Disable {
		// TOTP related endpoints.
		r.GET("/api/user/info/totp", middleware(middlewares.Require1FA(handlers.UserTOTPInfoGET)))
		r.POST("/api/secondfactor/totp/identity/start", middleware(middlewares.Require1FA(handlers.TOTPIdentityStart)))
		r.POST("/api/secondfactor/totp/identity/finish", middleware(middlewares.Require1FA(handlers.TOTPIdentityFinish)))
		r.POST("/api/secondfactor/totp", middleware(middlewares.Require1FA(handlers.TimeBasedOneTimePasswordPOST)))
	}

	if !config.Webauthn.Disable {
		// Webauthn Endpoints.
		r.POST("/api/secondfactor/webauthn/identity/start", middleware(middlewares.Require1FA(handlers.WebauthnIdentityStart)))
		r.POST("/api/secondfactor/webauthn/identity/finish", middleware(middlewares.Require1FA(handlers.WebauthnIdentityFinish)))
		r.POST("/api/secondfactor/webauthn/attestation", middleware(middlewares.Require1FA(handlers.WebauthnAttestationPOST)))

		r.GET("/api/secondfactor/webauthn/assertion", middleware(middlewares.Require1FA(handlers.WebauthnAssertionGET)))
		r.POST("/api/secondfactor/webauthn/assertion", middleware(middlewares.Require1FA(handlers.WebauthnAssertionPOST)))
	}

	// Configure DUO api endpoint only if configuration exists.
	if config.DuoAPI != nil {
		var duoAPI duo.API
		if os.Getenv("ENVIRONMENT") == dev {
			duoAPI = duo.NewDuoAPI(duoapi.NewDuoApi(
				config.DuoAPI.IntegrationKey,
				config.DuoAPI.SecretKey,
				config.DuoAPI.Hostname, "", duoapi.SetInsecure()))
		} else {
			duoAPI = duo.NewDuoAPI(duoapi.NewDuoApi(
				config.DuoAPI.IntegrationKey,
				config.DuoAPI.SecretKey,
				config.DuoAPI.Hostname, ""))
		}

		r.GET("/api/secondfactor/duo_devices", middleware(middlewares.Require1FA(handlers.DuoDevicesGET(duoAPI))))
		r.POST("/api/secondfactor/duo", middleware(middlewares.Require1FA(handlers.DuoPOST(duoAPI))))
		r.POST("/api/secondfactor/duo_device", middleware(middlewares.Require1FA(handlers.DuoDevicePOST)))
	}

	if config.Server.EnablePprof {
		r.GET("/debug/pprof/{name?}", pprofhandler.PprofHandler)
	}

	if config.Server.EnableExpvars {
		r.GET("/debug/vars", expvarhandler.ExpvarHandler)
	}

	if providers.OpenIDConnect.Fosite != nil {
		r.GET("/api/oidc/consent", middleware(handlers.OpenIDConnectConsentGET))
		r.POST("/api/oidc/consent", middleware(handlers.OpenIDConnectConsentPOST))

		allowedOrigins := utils.StringSliceFromURLs(config.IdentityProviders.OIDC.CORS.AllowedOrigins)

		r.OPTIONS(oidc.WellKnownOpenIDConfigurationPath, policyCORSPublicGET.HandleOPTIONS)
		r.GET(oidc.WellKnownOpenIDConfigurationPath, policyCORSPublicGET.Middleware(middleware(handlers.OpenIDConnectConfigurationWellKnownGET)))

		r.OPTIONS(oidc.WellKnownOAuthAuthorizationServerPath, policyCORSPublicGET.HandleOPTIONS)
		r.GET(oidc.WellKnownOAuthAuthorizationServerPath, policyCORSPublicGET.Middleware(middleware(handlers.OAuthAuthorizationServerWellKnownGET)))

		r.OPTIONS(oidc.JWKsPath, policyCORSPublicGET.HandleOPTIONS)
		r.GET(oidc.JWKsPath, policyCORSPublicGET.Middleware(middleware(handlers.JSONWebKeySetGET)))

		// TODO (james-d-elliott): Remove in GA. This is a legacy implementation of the above endpoint.
		r.OPTIONS("/api/oidc/jwks", policyCORSPublicGET.HandleOPTIONS)
		r.GET("/api/oidc/jwks", policyCORSPublicGET.Middleware(middleware(handlers.JSONWebKeySetGET)))

		policyCORSAuthorization := middlewares.NewCORSPolicyBuilder().
			WithAllowedMethods("OPTIONS", "GET").
			WithAllowedOrigins(allowedOrigins...).
			WithEnabled(utils.IsStringInSlice(oidc.AuthorizationEndpoint, config.IdentityProviders.OIDC.CORS.Endpoints)).
			Build()

		r.OPTIONS(oidc.AuthorizationPath, policyCORSAuthorization.HandleOnlyOPTIONS)
		r.GET(oidc.AuthorizationPath, middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OpenIDConnectAuthorizationGET)))

		// TODO (james-d-elliott): Remove in GA. This is a legacy endpoint.
		r.OPTIONS("/api/oidc/authorize", policyCORSAuthorization.HandleOnlyOPTIONS)
		r.GET("/api/oidc/authorize", middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OpenIDConnectAuthorizationGET)))

		policyCORSToken := middlewares.NewCORSPolicyBuilder().
			WithAllowCredentials(true).
			WithAllowedMethods("OPTIONS", "POST").
			WithAllowedOrigins(allowedOrigins...).
			WithEnabled(utils.IsStringInSlice(oidc.TokenEndpoint, config.IdentityProviders.OIDC.CORS.Endpoints)).
			Build()

		r.OPTIONS(oidc.TokenPath, policyCORSToken.HandleOPTIONS)
		r.POST(oidc.TokenPath, policyCORSToken.Middleware(middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OpenIDConnectTokenPOST))))

		policyCORSUserinfo := middlewares.NewCORSPolicyBuilder().
			WithAllowCredentials(true).
			WithAllowedMethods("OPTIONS", "GET", "POST").
			WithAllowedOrigins(allowedOrigins...).
			WithEnabled(utils.IsStringInSlice(oidc.UserinfoEndpoint, config.IdentityProviders.OIDC.CORS.Endpoints)).
			Build()

		r.OPTIONS(oidc.UserinfoPath, policyCORSUserinfo.HandleOPTIONS)
		r.GET(oidc.UserinfoPath, policyCORSUserinfo.Middleware(middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OpenIDConnectUserinfo))))
		r.POST(oidc.UserinfoPath, policyCORSUserinfo.Middleware(middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OpenIDConnectUserinfo))))

		policyCORSIntrospection := middlewares.NewCORSPolicyBuilder().
			WithAllowCredentials(true).
			WithAllowedMethods("OPTIONS", "POST").
			WithAllowedOrigins(allowedOrigins...).
			WithEnabled(utils.IsStringInSlice(oidc.IntrospectionEndpoint, config.IdentityProviders.OIDC.CORS.Endpoints)).
			Build()

		r.OPTIONS(oidc.IntrospectionPath, policyCORSIntrospection.HandleOPTIONS)
		r.POST(oidc.IntrospectionPath, policyCORSIntrospection.Middleware(middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OAuthIntrospectionPOST))))

		// TODO (james-d-elliott): Remove in GA. This is a legacy implementation of the above endpoint.
		r.OPTIONS("/api/oidc/introspect", policyCORSIntrospection.HandleOPTIONS)
		r.POST("/api/oidc/introspect", policyCORSIntrospection.Middleware(middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OAuthIntrospectionPOST))))

		policyCORSRevocation := middlewares.NewCORSPolicyBuilder().
			WithAllowCredentials(true).
			WithAllowedMethods("OPTIONS", "POST").
			WithAllowedOrigins(allowedOrigins...).
			WithEnabled(utils.IsStringInSlice(oidc.RevocationEndpoint, config.IdentityProviders.OIDC.CORS.Endpoints)).
			Build()

		r.OPTIONS(oidc.RevocationPath, policyCORSRevocation.HandleOPTIONS)
		r.POST(oidc.RevocationPath, policyCORSRevocation.Middleware(middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OAuthRevocationPOST))))

		// TODO (james-d-elliott): Remove in GA. This is a legacy implementation of the above endpoint.
		r.OPTIONS("/api/oidc/revoke", policyCORSRevocation.HandleOPTIONS)
		r.POST("/api/oidc/revoke", policyCORSRevocation.Middleware(middleware(middlewares.NewHTTPToAutheliaHandlerAdaptor(handlers.OAuthRevocationPOST))))
	}

	r.NotFound = handlerNotFound(middleware(serveIndexHandler))

	r.HandleMethodNotAllowed = true
	r.MethodNotAllowed = handlerMethodNotAllowed

	handler := middlewares.LogRequestMiddleware(r.Handler)
	if config.Server.Path != "" {
		handler = middlewares.StripPathMiddleware(config.Server.Path, handler)
	}

	return handler
}
