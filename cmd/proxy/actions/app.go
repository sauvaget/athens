package actions

import (
	"fmt"

	"github.com/gobuffalo/buffalo"
	"github.com/gobuffalo/buffalo/middleware"
	"github.com/gobuffalo/buffalo/middleware/csrf"
	"github.com/gobuffalo/buffalo/middleware/i18n"
	"github.com/gobuffalo/buffalo/middleware/ssl"
	"github.com/gobuffalo/buffalo/render"
	"github.com/gobuffalo/packr"
	"github.com/gomods/athens/pkg/config/env"
	"github.com/gomods/athens/pkg/log"
	mw "github.com/gomods/athens/pkg/middleware"
	"github.com/gomods/athens/pkg/module"
	"github.com/rs/cors"
	"github.com/unrolled/secure"
)

// ENV is used to help switch settings based on where the
// application is being run. Default is "development".
var ENV = env.GoEnvironmentWithDefault("development")

// T is the translator to use
var T *i18n.Translator

func init() {
	proxy = render.New(render.Options{
		// HTML layout to be used for all HTML requests:
		HTMLLayout:       "application.html",
		JavaScriptLayout: "application.js",

		// Box containing all of the templates:
		TemplatesBox: packr.NewBox("../templates/proxy"),
		AssetsBox:    assetsBox,

		// Add template helpers here:
		Helpers: render.Helpers{},
	})
}

// App is where all routes and middleware for buffalo
// should be defined. This is the nerve center of your
// application.
func App() (*buffalo.App, error) {
	store, err := GetStorage()
	if err != nil {
		err = fmt.Errorf("error getting storage configuration (%s)", err)
		return nil, err
	}

	// mount .netrc to home dir
	// to have access to private repos.
	initializeNETRC()

	lvl, err := env.LogLevel()
	if err != nil {
		return nil, err
	}
	lggr := log.New(env.CloudRuntime(), lvl)

	blvl, err := env.BuffaloLogLevel()
	if err != nil {
		return nil, err
	}
	blggr := log.Buffalo(blvl)

	app := buffalo.New(buffalo.Options{
		Env: ENV,
		PreWares: []buffalo.PreWare{
			cors.Default().Handler,
		},
		SessionName: "_athens_session",
		Logger:      blggr,
		Addr:        env.Port(":3000"),
	})
	if prefix := env.AthensPathPrefix(); prefix != "" {
		// certain Ingress Controllers (such as GCP Load Balancer)
		// can not send custom headers and therefore if the proxy
		// is running behind a prefix as well as some authentication
		// mechanism, we should allow the plain / to return 200.
		app.GET("/", healthHandler)
		app = app.Group(prefix)
	}

	// Automatically redirect to SSL
	app.Use(ssl.ForceSSL(secure.Options{
		SSLRedirect:     env.ProxyForceSSL(),
		SSLProxyHeaders: map[string]string{"X-Forwarded-Proto": "https"},
	}))

	if ENV == "development" {
		app.Use(middleware.ParameterLogger)
	}
	initializeTracing(app)
	initializeAuth(app)
	// Protect against CSRF attacks. https://www.owasp.org/index.php/Cross-Site_Request_Forgery_(CSRF)
	// Remove to disable this.
	if env.EnableCSRFProtection() {
		csrfMiddleware := csrf.New
		app.Use(csrfMiddleware)
	}
	// Setup and use translations:
	if T, err = i18n.New(packr.NewBox("../locales"), "en-US"); err != nil {
		app.Stop(err)
	}
	app.Use(T.Middleware())

	if !env.FilterOff() {
		mf := module.NewFilter()
		app.Use(mw.NewFilterMiddleware(mf))
	}

	// Having the hook set means we want to use it
	if validatorHook, ok := env.ValidatorHook(); ok {
		app.Use(mw.LogEntryMiddleware(mw.NewValidationMiddleware, lggr, validatorHook))
	}

	user, pass, ok := env.BasicAuth()
	if ok {
		app.Use(basicAuth(user, pass))
	}

	if err := addProxyRoutes(app, store, lggr); err != nil {
		err = fmt.Errorf("error adding proxy routes (%s)", err)
		return nil, err
	}

	// serve files from the public directory:
	// has to be last
	app.ServeFiles("/", assetsBox)

	return app, nil
}
