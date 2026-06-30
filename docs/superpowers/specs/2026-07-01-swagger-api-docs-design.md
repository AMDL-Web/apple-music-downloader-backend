# Swagger API Docs Design

Expose the complete AMDL HTTP contract as an embedded OpenAPI 3.1 document at `/api/openapi.yaml`. Serve Swagger UI at `/docs` using pinned `swagger-ui-dist@5.32.8` CDN assets, with “Try it out” targeting the same API origin.

The specification covers health, capabilities, wrapper authentication, downloads, cancellation, and SSE events. Documentation examples use only fictional credentials. Route tests verify the UI wiring and parse the embedded specification so malformed or incomplete docs fail CI.
