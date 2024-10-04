package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/donseba/go-router"
	"github.com/donseba/go-router/middleware"
)

type (
	User struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	Blog struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
)

func main() {
	mux := http.NewServeMux()
	r := router.New(mux, "Example API", "1.0.0")

	r.AddServerEndpoint("http://localhost:3210", "Demonstration of the example API.")

	r.UseOpenapiDocs(true)
	// Apply global middleware
	r.Use(middleware.Timer)
	r.Use(middleware.Recover)

	// Serve static files
	r.ServeFiles("/file/", http.Dir("./files"))
	r.ServeFile("/favicon.ico", "./files/favicon.ico")

	// Set custom handlers from methods
	r.HandleStatus(http.StatusNotFound, notFoundHandler)
	r.HandleStatus(http.StatusMethodNotAllowed, methodNotAllowedHandler)

	r.RedirectTrailingSlash(true)

	// set custom handler inlining
	r.HandleStatus(http.StatusInternalServerError, func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "Custom 500 - Internal Server Error", http.StatusInternalServerError)
	})

	// Define routes
	r.Get("/{$}", homeHandler, router.Docs{
		Summary:     "Home Page",
		Description: "Displays the home page.",
		Out: map[string]router.DocOut{
			"200": {
				ApplicationType: "text/html",
				Description:     "The home page.",
			},
		},
	})
	r.Get("/gopher", gopherHandler, router.Docs{
		Summary:     "Gopher Page",
		Description: "Displays a gopher image.",
		Out: map[string]router.DocOut{
			"200": {
				ApplicationType: "text/html",
				Description:     "The gopher image.",
			},
		},
	})
	r.Get("/panic", func(w http.ResponseWriter, req *http.Request) {
		panic("Panic!")
	}, router.Docs{
		Summary:     "Panic Page",
		Description: "Generates a panic.",
		Out: map[string]router.DocOut{
			"500": {
				ApplicationType: "text/plain",
				Description:     "Internal Server Error",
			},
		},
	})
	r.Post("/login", loginHandler)

	r.Group("/users", func(r *router.Router) {
		r.Get("", userListHandler, router.Docs{
			Summary:     "User List",
			Description: "Displays a list of users.",
			Out: map[string]router.DocOut{
				"200": {
					ApplicationType: "application/json",
					Description:     "The list of users.",
					Object:          []User{},
				},
			},
		})
		r.Get("/{id}", userHandler, router.Docs{
			Summary:     "Get User",
			Description: "Retrieves a user by ID.",
			Parameters: []router.Parameter{
				{
					Name:        "id",
					In:          "path",
					Description: "The ID of the user.",
					Schema: &router.Schema{
						Type: "string",
					},
					Required: true,
				},
			},
			Out: map[string]router.DocOut{
				"200": {
					ApplicationType: "application/json",
					Description:     "The user object.",
					Object:          User{},
				},
			},
		})

		r.Put("/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Update User")
		}, router.Docs{
			Summary:     "Update User",
			Description: "Updates an existing user.",
			In: map[string]router.DocIn{
				"application/json": {
					Object: User{},
				},
			},
			Out: map[string]router.DocOut{
				"200": {
					ApplicationType: "application/json",
					Description:     "The updated user object.",
					Object:          User{},
				},
			},
		})

		r.Post("", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Create User")
		}, router.Docs{
			Summary:     "Create User",
			Description: "Creates a new user.",
			In: map[string]router.DocIn{
				"application/json": {
					Object: User{},
				},
			},
			Out: map[string]router.DocOut{
				"201": {
					ApplicationType: "application/json",
					Description:     "The created user object.",
					Object:          User{},
				},
			},
		})
	})

	r.Group("/blog", func(r *router.Router) {
		r.Get("", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Blog List Page")
		}, router.Docs{
			Summary:     "Blog List",
			Description: "Displays a list of blog posts.",
			Out: map[string]router.DocOut{
				"200": {
					ApplicationType: "application/json",
					Description:     "The list of blog posts.",
					Object:          []Blog{},
				},
			},
		})

		r.Get("/{id}", func(w http.ResponseWriter, req *http.Request) {
			blogID := req.PathValue("id")
			_, _ = fmt.Fprintf(w, "Blog ID: %s", blogID)
		}, router.Docs{
			Summary:     "Get Blog",
			Description: "Retrieves a blog post by ID.",
			Parameters: []router.Parameter{
				{
					Name:        "id",
					In:          "path",
					Description: "The ID of the blog post.",
					Schema: &router.Schema{
						Type: "string",
					},
					Required: true,
				},
			},
			Out: map[string]router.DocOut{
				"200": {
					ApplicationType: "application/json",
					Description:     "The blog post object.",
					Object:          Blog{},
				},
			},
		})

		r.Post("", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Create Blog")
		}, router.Docs{
			Summary:     "Create Blog",
			Description: "Creates a new blog post.",
			In: map[string]router.DocIn{
				"application/json": {
					Object: Blog{},
				},
			},
			Out: map[string]router.DocOut{
				"201": {
					ApplicationType: "application/json",
					Description:     "The created blog post.",
					Object:          Blog{},
				},
			},
		})

		r.Put("/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Update Blog")
		}, router.Docs{
			Summary:     "Update Blog",
			Description: "Updates an existing blog post.",
			In: map[string]router.DocIn{
				"application/json": {
					Object: Blog{},
				},
			},
			Out: map[string]router.DocOut{
				"200": {
					ApplicationType: "application/json",
					Description:     "The updated blog post.",
					Object:          Blog{},
				},
			},
		})
	})

	r.Get("/docs", func(w http.ResponseWriter, req *http.Request) {
		docs := r.OpenAPI()

		out, err := json.MarshalIndent(docs, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		_, _ = fmt.Fprint(w, string(out))
	}, router.Docs{
		Summary:     "OpenAPI Docs",
		Description: "Displays the OpenAPI documentation.",
		Out: map[string]router.DocOut{
			"200": {
				ApplicationType: "application/json",
				Description:     "The OpenAPI documentation.",
			},
		},
	})

	// Start the server
	log.Println("Server is running at :3210")
	err := http.ListenAndServe(":3210", r)
	if err != nil {
		log.Fatal(err)
	}
}

func homeHandler(w http.ResponseWriter, req *http.Request) {
	_, _ = fmt.Fprintln(w, "Welcome to the Home Page")
}

func gopherHandler(w http.ResponseWriter, req *http.Request) {
	html := `<html>
	<head>
		<title>Gopher Page</title>
	</head>
	<body>
		<h1>Welcome to the Gopher Page</h1>
		<img src="/file/gopher.png" alt="Gopher" />
	</body>
	</html>`
	_, _ = fmt.Fprintln(w, html)
}

func loginHandler(w http.ResponseWriter, req *http.Request) {
	_, _ = fmt.Fprintln(w, "Login Page")
}

func userListHandler(w http.ResponseWriter, req *http.Request) {
	_, _ = fmt.Fprintln(w, "User List Page")
}

func userHandler(w http.ResponseWriter, req *http.Request) {
	userID := req.PathValue("id")
	_, _ = fmt.Fprintf(w, "User ID: %s", userID)
}

func notFoundHandler(w http.ResponseWriter, req *http.Request) {
	http.Error(w, "Custom 404 - Page Not Found", http.StatusNotFound)
}

func methodNotAllowedHandler(w http.ResponseWriter, req *http.Request) {
	http.Error(w, "Custom 405 - Method Not Allowed", http.StatusMethodNotAllowed)
}
