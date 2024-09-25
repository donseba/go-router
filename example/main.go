package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/donseba/go-router"
	"github.com/donseba/go-router/middleware"
)

func main() {
	r := router.NewDefault()

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
		Title:       "Home Page",
		Description: "Displays the home page.",
	}) // only listen to the root path
	r.Get("/gopher", gopherHandler, router.Docs{
		Title:       "Gopher Page",
		Description: "Displays a gopher image.",
	})
	r.Post("/login", loginHandler)

	r.Group("/users", func(r *router.Router) {
		r.Get("", userListHandler)
		r.Get("/{id}", userHandler, router.Docs{
			Title:       "Get User",
			Description: "Retrieves a user by ID.",
			Params: []router.DocsParam{
				{Name: "id", Type: "string", Description: "The ID of the user."},
			},
		})
	})

	r.Get("/docs", func(w http.ResponseWriter, req *http.Request) {
		docs := r.GetDocs()

		out, err := json.MarshalIndent(docs, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		_, _ = fmt.Fprint(w, string(out))
	}, router.Docs{
		Title:       "API Documentation",
		Description: "Returns the API documentation.",
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
