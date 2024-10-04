package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/donseba/go-router"
	"github.com/donseba/go-router/middleware"
)

func main() {
	mux := http.NewServeMux()
	r := router.New(mux, "Example API", "1.0.0")

	// Apply global middleware
	r.Use(middleware.Timer)
	r.Use(middleware.Recover)

	// Serve static files
	r.ServeFiles("/file/", http.Dir("./files"))
	r.ServeFile("/favicon.ico", "./files/favicon.ico")

	// Set custom handlers from methods
	r.HandleStatus(http.StatusNotFound, notFoundHandler)
	r.HandleStatus(http.StatusMethodNotAllowed, methodNotAllowedHandler)

	// set custom handler inlining
	r.HandleStatus(http.StatusInternalServerError, func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "Custom 500 - Internal Server Error", http.StatusInternalServerError)
	})

	// Define routes
	r.Get("/{$}", homeHandler)
	r.Get("/gopher", gopherHandler)
	r.Post("/login", loginHandler)

	r.Group("/users", func(r *router.Router) {
		// custom middleware for this group
		r.Use(func(next http.Handler) http.Handler {
			fn := func(w http.ResponseWriter, r *http.Request) {
				log.Print("[go-router] user middleware")

				next.ServeHTTP(w, r)

			}

			return http.HandlerFunc(fn)
		})
		r.Get("", userListHandler)
		r.Get("/{id}", userHandler)

		r.Put("/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Update User")
		})

		r.Post("", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "Create User")
		})
	})

	r.Get("/panic", func(w http.ResponseWriter, req *http.Request) {
		panic("Panic!")
	})

	// Start the server
	log.Println("Server is running at :3211")
	err := http.ListenAndServe(":3211", r)
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
