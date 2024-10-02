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
)

func main() {
	mux := http.NewServeMux()
	r := router.New(mux, "Example API", "1.0.0")

	r.AddServerEndpoint("http://localhost:3210", "Demonstration of the example API.")

	r.UseOpenapiDocs(true)
	// Apply global middleware
	r.Use(middleware.Timer)

	// Serve static files
	r.ServeFiles("/file/", http.Dir("./files"))
	r.ServeFile("/favicon.ico", "./files/favicon.ico")

	// Set custom handlers
	r.NotFound(notFoundHandler)
	r.MethodNotAllowed(methodNotAllowedHandler)

	// Define routes
	r.Get("/{$}", homeHandler, router.Docs{
		Summary:     "Home Page",
		Description: "Displays the home page.",
	})
	r.Get("/gopher", gopherHandler, router.Docs{
		Summary:     "Gopher Page",
		Description: "Displays a gopher image.",
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
		})

		r.Post("", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Create User")
		}, router.Docs{
			Summary:     "Create User",
			Description: "Creates a new user.",
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
