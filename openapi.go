package router

// OpenAPI represents the root OpenAPI document.
type OpenAPI struct {
	Openapi    string                `json:"openapi" validate:"required"` // OpenAPI version (e.g., "3.0.1")
	Info       Info                  `json:"info" validate:"required"`    // API information
	Servers    []Server              `json:"servers,omitempty"`           // Server details
	Paths      map[string]PathItem   `json:"paths" validate:"required"`   // Paths and operations
	Components Components            `json:"components,omitempty"`        // Components such as schemas and security schemes
	Security   []map[string][]string `json:"security,omitempty"`          // Global security settings
	Tags       []Tag                 `json:"tags,omitempty"`              // Tags for API organization
}

// Info represents the API metadata.
type Info struct {
	Title       string `json:"title" validate:"required"`   // API title
	Description string `json:"description,omitempty"`       // API description
	Version     string `json:"version" validate:"required"` // API version
}

// Server represents an API server.
type Server struct {
	URL         string            `json:"url" validate:"required"` // Server URL
	Description string            `json:"description,omitempty"`   // Server description
	Variables   map[string]string `json:"variables,omitempty"`     // Variables for URL template
}

// PathItem describes the operations available on a single path.
type PathItem struct {
	Get    *Operation `json:"get,omitempty"`    // GET operation
	Post   *Operation `json:"post,omitempty"`   // POST operation
	Put    *Operation `json:"put,omitempty"`    // PUT operation
	Delete *Operation `json:"delete,omitempty"` // DELETE operation
	Patch  *Operation `json:"patch,omitempty"`  // PATCH operation
}

func (p PathItem) Methods() []string {
	var methods []string
	if p.Get != nil {
		methods = append(methods, "GET")
	}
	if p.Post != nil {
		methods = append(methods, "POST")
	}
	if p.Put != nil {
		methods = append(methods, "PUT")
	}
	if p.Delete != nil {
		methods = append(methods, "DELETE")
	}
	if p.Patch != nil {
		methods = append(methods, "PATCH")
	}
	return methods
}

func (p PathItem) SetMethod(method string, operation *Operation) PathItem {
	switch method {
	case "GET":
		p.Get = operation
	case "POST":
		p.Post = operation
	case "PUT":
		p.Put = operation
	case "DELETE":
		p.Delete = operation
	case "PATCH":
		p.Patch = operation
	}

	return p
}

// Operation describes a single API operation on a path.
type Operation struct {
	Tags        []string              `json:"tags,omitempty"`                // Tags for the operation
	Summary     string                `json:"summary,omitempty"`             // Short summary of the operation
	Description string                `json:"description,omitempty"`         // Operation description
	OperationID string                `json:"operationId,omitempty"`         // Unique operation ID
	Parameters  []Parameter           `json:"parameters,omitempty"`          // Parameters for the operation
	RequestBody *RequestBody          `json:"requestBody,omitempty"`         // Request body for the operation
	Responses   map[string]Response   `json:"responses" validate:"required"` // Expected responses
	Security    []map[string][]string `json:"security,omitempty"`            // Security requirements
}

// Parameter represents a single parameter for an operation.
type Parameter struct {
	Name        string  `json:"name" validate:"required"` // Parameter name
	In          string  `json:"in" validate:"required"`   // Location (e.g., "query", "header", "path")
	Description string  `json:"description,omitempty"`    // Parameter description
	Required    bool    `json:"required,omitempty"`       // Is parameter required?
	Schema      *Schema `json:"schema,omitempty"`         // Schema defining the type
}

// RequestBody represents a request body for an operation.
type RequestBody struct {
	Description string               `json:"description,omitempty"` // Request body description
	Content     map[string]MediaType `json:"content"`               // Media types supported by the request
	Required    bool                 `json:"required,omitempty"`    // Is request body required?
}

// Response describes a single response from an API operation.
type Response struct {
	Description string               `json:"description" validate:"required"` // Response description
	Content     map[string]MediaType `json:"content,omitempty"`               // Media types produced by the response
}

// MediaType represents the media type of a request or response body.
type MediaType struct {
	Schema *Schema `json:"schema,omitempty"` // Schema describing the type
}

// Schema represents the structure of a request or response body.
type Schema struct {
	Ref        string            `json:"$ref,omitempty"`       // Reference to a schema
	Type       string            `json:"type,omitempty"`       // Data type (e.g., "string", "object")
	Format     string            `json:"format,omitempty"`     // Data format (e.g., "uuid", "email")
	Properties map[string]Schema `json:"properties,omitempty"` // Properties of the object
	Items      *Schema           `json:"items,omitempty"`      // Schema for array items
	Required   []string          `json:"required,omitempty"`   // Required properties
}

// Components holds reusable components such as schemas and security schemes.
type Components struct {
	Schemas         map[string]Schema         `json:"schemas,omitempty"`         // Reusable schemas
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty"` // Security schemes
}

// SecurityScheme defines a security scheme for the API.
type SecurityScheme struct {
	Type         string `json:"type" validate:"required"` // Security scheme type (e.g., "http", "apiKey")
	Scheme       string `json:"scheme,omitempty"`         // HTTP Authorization scheme (e.g., "bearer")
	BearerFormat string `json:"bearerFormat,omitempty"`   // Bearer token format
}

// Tag represents a tag for an API operation.
type Tag struct {
	Name        string `json:"name" validate:"required"` // Tag name
	Description string `json:"description,omitempty"`    // Tag description
}
