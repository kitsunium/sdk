// Package tlsid loads TLS and mutual-TLS material into the opaque, redacting
// identity declared by internal/core/net (ADR 0029). It is the service half of
// the domain's TLS surface: this package does the filesystem I/O and nothing
// else, delegating every validation decision to core so that in-memory and
// on-disk material are held to exactly the same standard.
package tlsid

import (
	"os"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Load reads the named TLS material and returns the validated identity.
//
// A missing or unreadable file is TLSMaterialInvalid naming the offending
// field, never a silently skipped one: a trust bundle that fails to load must
// not degrade into "trust the platform", and a client keypair that fails to
// load must not degrade into an anonymous connection.
func Load(p FileParams) (corenet.IdentityValue, error) {
	certPEM, err := readMaterial(p.CertFile, "cert_file")
	if err != nil {
		return corenet.IdentityValue{}, err
	}
	keyPEM, err := readMaterial(p.KeyFile, "key_file")
	if err != nil {
		return corenet.IdentityValue{}, err
	}
	rootsPEM, err := readMaterial(p.RootsFile, "roots_file")
	if err != nil {
		return corenet.IdentityValue{}, err
	}
	clientCAPEM, err := readMaterial(p.ClientCAFile, "client_ca_file")
	if err != nil {
		return corenet.IdentityValue{}, err
	}
	//: every validation rule lives in core, so disk-sourced and memory-sourced
	//: material are held to the same standard by construction.
	return corenet.NewIdentity(corenet.IdentityParams{
		CertPEM:           certPEM,
		KeyPEM:            keyPEM,
		RootsPEM:          rootsPEM,
		ClientCAPEM:       clientCAPEM,
		ServerName:        p.ServerName,
		MinVersion:        p.MinVersion,
		NextProtos:        p.NextProtos,
		RequireClientCert: p.RequireClientCert,
	})
}

// readMaterial reads one optional PEM file, distinguishing "not configured"
// from "configured but unreadable".
func readMaterial(path, field string) ([]byte, error) {
	//: an empty path means the caller did not configure this material at all.
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		//: the path is echoed because it is operator-supplied configuration, not
		//: a secret; the file's contents never are.
		return nil, wrapMaterial(err, field, path)
	}
	//: a present but empty file is a deployment slip — an empty trust bundle
	//: would otherwise reach core as "not configured" and silently widen trust.
	if len(raw) == 0 {
		return nil, wrapMaterial(nil, field, path)
	}
	return raw, nil
}

// wrapMaterial reports a material-loading failure as the core sentinel, so the
// domain code survives whatever the filesystem returned.
func wrapMaterial(cause error, field, path string) error {
	fields := []errs.FieldValue{errs.String("field", field), errs.String("path", path)}
	//: a nil cause means the file read fine but carried nothing usable.
	if cause == nil {
		return errs.Wrap(corenet.TLSMaterialInvalid, errs.WrapParams{}, fields...)
	}
	return errs.Wrap(corenet.TLSMaterialInvalid, errs.WrapParams{},
		append(fields, errs.String("cause", cause.Error()))...)
}
