package server

import "net/http"

func incomingConnection(w http.ResponseWriter, r *http.Request) {
	// check if we have reached max connections if set
	if maxConnections > 0 {
		defer nbConn.Add(-1)
		if nbConn.Add(1) > maxConnections {
			// send error
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
	}
}
