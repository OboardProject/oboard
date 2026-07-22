package controller

import "github.com/OboardProject/oboard/internal/store"

func newTestServer(store *store.Store, sessionSecret, staticDir string) *Server {
	return New(store, sessionSecret, staticDir, "", nil)
}
