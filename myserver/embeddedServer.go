package myserver

import (
	"crypto/tls"
	_ "embed"
	"net"
	"net/http"
)

//go:embed server.crt
var c string

//go:embed server.key
var k string

type EmbeddedServer struct {
	http.Server
	Certificate string
	Key         string
}

func (srv *EmbeddedServer) ListenAndServeTLS(addr string, handler http.Handler) error {

	config := &tls.Config{
		MinVersion: tls.VersionTLS10,
	}
	if srv.TLSConfig != nil {
		*config = *srv.TLSConfig
	}
	if config.NextProtos == nil {
		config.NextProtos = []string{"http/1.1"}
	}

	var err error
	config.Certificates = make([]tls.Certificate, 1)
	config.Certificates[0], err = tls.X509KeyPair([]byte(srv.Certificate), []byte(srv.Key))
	if err != nil {
		return err
	}

	conn, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	tlsListener := tls.NewListener(conn, config)
	srv.Handler = handler
	return srv.Serve(tlsListener)
}
