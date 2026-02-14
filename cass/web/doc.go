// Package web provides an embedded HTTP server for CASS with a Preact UI.
//
// The server wraps [service.Service] methods as JSON API endpoints and
// uses Server-Sent Events to push live session updates to the browser.
// Static assets are embedded via go:embed and use Preact+HTM loaded
// from esm.sh (no build step required).
//
// # Usage
//
//	srv := web.New(web.Config{
//		Service: svc,
//		Addr:    ":8080",
//	})
//	srv.Start(ctx)
//
// # Development
//
// Use DevMode to serve static files from disk for live reload:
//
//	srv := web.New(web.Config{
//		Service: svc,
//		DevMode: true,
//	})
package web
