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
func Load(p corenet.IdentityFileParams) (id corenet.IdentityValue, err error) {
	certPEM, err := readMaterial(p.CertFile, "cert_file")
	//: a configured-but-unreadable file must not be skipped silently.
	if err != nil {
		//: surface TLS_MATERIAL_INVALID naming the offending field.
		return corenet.IdentityValue{}, err
	}
	keyPEM, err := readMaterial(p.KeyFile, "key_file")
	//: a configured-but-unreadable file must not be skipped silently.
	if err != nil {
		//: surface TLS_MATERIAL_INVALID naming the offending field.
		return corenet.IdentityValue{}, err
	}
	rootsPEM, err := readMaterial(p.RootsFile, "roots_file")
	//: a configured-but-unreadable file must not be skipped silently.
	if err != nil {
		//: surface TLS_MATERIAL_INVALID naming the offending field.
		return corenet.IdentityValue{}, err
	}
	clientCAPEM, err := readMaterial(p.ClientCAFile, "client_ca_file")
	//: a configured-but-unreadable file must not be skipped silently.
	if err != nil {
		//: surface TLS_MATERIAL_INVALID naming the offending field.
		return corenet.IdentityValue{}, err
	}
	//: every validation rule lives in core, so disk-sourced and memory-sourced
	//: material are held to the same standard by construction.
	return corenet.NewIdentityValue(corenet.IdentityParams{
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
func readMaterial(path, field string) (raw []byte, err error) {
	//: an empty path means the caller did not configure this material at all.
	if path == "" {
		//: nil bytes mean "the caller did not ask for this material".
		return nil, nil
	}
	content, rerr := os.ReadFile(path)
	//: the file was named, so failing to read it is a hard error.
	if rerr != nil {
		//: the path is echoed because it is operator-supplied configuration, not
		//: a secret; the file's contents never are.
		return nil, wrapMaterial(rerr, field, path)
	}
	//: a present but empty file is a deployment slip — an empty trust bundle
	//: would otherwise reach core as "not configured" and silently widen trust.
	if len(content) == 0 {
		//: an empty trust bundle would otherwise widen trust silently.
		return nil, wrapMaterial(nil, field, path)
	}
	//: the file exists and carries something to parse.
	return content, nil
}

// wrapMaterial reports a material-loading failure as the core sentinel, so the
// domain code survives whatever the filesystem returned.
func wrapMaterial(cause error, field, path string) error {
	fields := []errs.FieldValue{errs.String("field", field), errs.String("path", path)}
	//: a nil cause means the file read fine but carried nothing usable.
	if cause == nil {
		//: nothing to quote — the file read fine but carried nothing usable.
		return errs.Wrap(corenet.TLSMaterialInvalid, errs.WrapParams{}, fields...)
	}
	//: carry the OS error so the operator sees why the read failed.
	return errs.Wrap(corenet.TLSMaterialInvalid, errs.WrapParams{},
		append(fields, errs.String("cause", cause.Error()))...)
}
