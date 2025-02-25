// 🚀 Fiber is an Express inspired web framework written in Go with 💖
// 📌 API Documentation: https://fiber.wiki
// 📝 Github Repository: https://github.com/gofiber/fiber

package fiber

import (
	"bytes"
	"log"
	"regexp"
	"strings"
	"text/template"
	"time"

	fasthttp "github.com/valyala/fasthttp"
)

// Route struct
type Route struct {
	isGet bool // allows HEAD requests if GET

	isMiddleware bool // is middleware route

	isStar  bool // path == '*'
	isSlash bool // path == '/'
	isRegex bool // needs regex parsing

	Method string         // http method
	Path   string         // orginal path
	Params []string       // path params
	Regexp *regexp.Regexp // regexp matcher

	Handler func(*Ctx) // ctx handler
}

func (app *App) nextRoute(ctx *Ctx) {
	// Keep track of head matches
	lenr := len(app.routes) - 1
	for ctx.index < lenr {
		ctx.index++
		route := app.routes[ctx.index]
		match, values := route.matchRoute(ctx.method, ctx.path)
		if match {
			ctx.route = route
			ctx.values = values
			route.Handler(ctx)
			return
		}
	}
	if len(ctx.Fasthttp.Response.Body()) == 0 {
		ctx.SendStatus(404)
	}
}

func (r *Route) matchRoute(method, path string) (match bool, values []string) {
	// is route middleware? matches all http methods
	if r.isMiddleware {
		// '*' or '/' means its a valid match
		if r.isStar || r.isSlash {
			return true, values
		}
		// if midware path starts with req.path
		if strings.HasPrefix(path, r.Path) {
			return true, values
		}
		// middlewares dont support regex so bye!
		return false, values
	}
	// non-middleware route, http method must match!
	// the wildcard method is for .All() & .Use() methods
	// If route is GET, also match HEAD requests
	if r.Method == method || r.Method[0] == '*' || (r.isGet && len(method) == 4 && method == "HEAD") {
		// '*' means we match anything
		if r.isStar {
			return true, values
		}
		// simple '/' bool, so you avoid unnecessary comparison for long paths
		if r.isSlash && path == "/" {
			return true, values
		}
		// does this route need regex matching?
		// does req.path match regex pattern?
		if r.isRegex && r.Regexp.MatchString(path) {
			// do we have parameters
			if len(r.Params) > 0 {
				// get values for parameters
				matches := r.Regexp.FindAllStringSubmatch(path, -1)
				// did we get the values?
				if len(matches) > 0 && len(matches[0]) > 1 {
					values = matches[0][1:len(matches[0])]
					return true, values
				}
				return false, values
			}
			return true, values
		}
		// last thing to do is to check for a simple path match
		if len(r.Path) == len(path) && r.Path == path {
			return true, values
		}
	}
	// Nothing match
	return false, values
}

func (app *App) handler(fctx *fasthttp.RequestCtx) {
	// get fiber context from sync pool
	ctx := acquireCtx(fctx)
	defer releaseCtx(ctx)
	// attach app poiner and compress settings
	ctx.app = app

	// Case sensitive routing
	if !app.Settings.CaseSensitive {
		ctx.path = strings.ToLower(ctx.path)
	}
	// Strict routing
	if !app.Settings.StrictRouting && len(ctx.path) > 1 {
		ctx.path = strings.TrimRight(ctx.path, "/")
	}
	// Find route
	app.nextRoute(ctx)
}

func (app *App) registerMethod(method, path string, handlers ...func(*Ctx)) {
	// Route requires atleast one handler
	if len(handlers) == 0 {
		log.Fatalf("Missing handler in route")
	}
	// Cannot have an empty path
	if path == "" {
		path = "/"
	}
	// Path always start with a '/' or '*'
	if path[0] != '/' && path[0] != '*' {
		path = "/" + path
	}
	// Store original path to strip case sensitive params
	original := path
	// Case sensitive routing, all to lowercase
	if !app.Settings.CaseSensitive {
		path = strings.ToLower(path)
	}
	// Strict routing, remove last `/`
	if !app.Settings.StrictRouting && len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	// Set route booleans
	var isGet = method == "GET"
	var isMiddleware = method == "USE"
	// Middleware / All allows all HTTP methods
	if isMiddleware || method == "ALL" {
		method = "*"
	}
	var isStar = path == "*" || path == "/*"
	// Middleware containing only a `/` equals wildcard
	if isMiddleware && path == "/" {
		isStar = true
	}
	var isSlash = path == "/"
	var isRegex = false
	// Route properties
	var Params = getParams(original)
	var Regexp *regexp.Regexp
	// Params requires regex pattern
	if len(Params) > 0 {
		regex, err := getRegex(path)
		if err != nil {
			log.Fatal("Router: Invalid path pattern: " + path)
		}
		isRegex = true
		Regexp = regex
	}
	for i := range handlers {
		app.routes = append(app.routes, &Route{
			isGet:        isGet,
			isMiddleware: isMiddleware,
			isStar:       isStar,
			isSlash:      isSlash,
			isRegex:      isRegex,
			Method:       method,
			Path:         path,
			Params:       Params,
			Regexp:       Regexp,
			Handler:      handlers[i],
		})
	}
}

func (app *App) registerStatic(prefix, pattern string, config ...Static) {
	// Cannot have an empty prefix
	if prefix == "" {
		prefix = "/"
	}
	// Prefix always start with a '/' or '*'
	if prefix[0] != '/' && prefix[0] != '*' {
		prefix = "/" + prefix
	}
	// Match anything
	var wildcard = false
	if prefix == "*" || prefix == "/*" {
		wildcard = true
		prefix = "/"
	}
	// Case sensitive routing, all to lowercase
	if !app.Settings.CaseSensitive {
		prefix = strings.ToLower(prefix)
	}
	// For security we want to restrict to the current work directory.
	if len(pattern) == 0 {
		pattern = "."
	}
	// Strip trailing slashes from the root path
	if len(pattern) > 0 && pattern[len(pattern)-1] == '/' {
		pattern = pattern[:len(pattern)-1]
	}
	// isSlash ?
	var isSlash = prefix == "/"
	if strings.Contains(prefix, "*") {
		wildcard = true
		prefix = strings.Split(prefix, "*")[0]
	}
	var stripper = len(prefix)
	if isSlash {
		stripper = 0
	}

	tmpl, err := template.New("directoryPattern").Parse(pattern)
	if err != nil {
		panic(err)
	}

	fileHandlerByPath := make(map[string]fasthttp.RequestHandler)

	app.routes = append(app.routes, &Route{
		isMiddleware: true,
		isSlash:      isSlash,
		Method:       "*",
		Path:         prefix,
		Handler: func(c *Ctx) {
			// Only handle GET & HEAD methods
			if c.method == "GET" || c.method == "HEAD" {
				// Do stuff
				if wildcard {
					c.Fasthttp.Request.SetRequestURI(prefix)
				}

				var tpl bytes.Buffer
				execErr := tmpl.Execute(&tpl, c)
				if execErr != nil {
					panic(execErr)
				}

				root := tpl.String()
				handler, handlerExist := fileHandlerByPath[root]
				if !handlerExist {
					handler = createHandler(root, stripper, config...)
					fileHandlerByPath[root] = handler
				}

				// Serve file
				handler(c.Fasthttp)
				// End response when file is found
				if c.Fasthttp.Response.StatusCode() != 404 {
					return
				}
				// Reset response
				c.Fasthttp.Response.Reset()
			}
			c.Next()
		},
	})
}

func createHandler(root string, stripper int, config ...Static) fasthttp.RequestHandler {
	fs := &fasthttp.FS{
		Root:                 root,
		GenerateIndexPages:   false,
		AcceptByteRange:      false,
		Compress:             false,
		CompressedFileSuffix: ".fiber.gz",
		CacheDuration:        10 * time.Second,
		IndexNames:           []string{"index.html"},
		PathRewrite:          fasthttp.NewPathPrefixStripper(stripper),
		PathNotFound: func(ctx *fasthttp.RequestCtx) {
			ctx.Response.SetStatusCode(404)
			ctx.Response.SetBodyString("Not Found")
		},
	}
	// Set config if provided
	if len(config) > 0 {
		fs.Compress = config[0].Compress
		fs.AcceptByteRange = config[0].ByteRange
		fs.GenerateIndexPages = config[0].Browse
		if config[0].Index != "" {
			fs.IndexNames = []string{config[0].Index}
		}
	}

	return fs.NewRequestHandler()
}
