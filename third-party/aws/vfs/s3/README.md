# s3 — a vfs filesystem over an S3 bucket

`import "github.com/kitsunium/sdk/third-party/aws/vfs/s3"` (module `github.com/kitsunium/sdk/third-party/aws`)

A `vfs.FullFS` over one bucket, or one key prefix of it: `io/fs` reading
unchanged, and `WriteAtomic` as one `PutObject`, which S3 makes atomic per key.

```go
filesystem, err := s3.New(s3.Config{
	Bucket: "assets", Prefix: "site", Region: "eu-west-3",
	Credentials: provider, // a writer.CredentialProvider
	MaxKeys: 1000, Timeout: 10 * time.Second,
})
if err != nil {
	return err
}
err = filesystem.WriteAtomic("index.html", page, 0o644)
```

Modes are recorded as object metadata and enforced by nobody; a directory is
a marker object or a prefix; a file is at most 5 GiB. The package comment and
`CLAUDE.md` give the whole mapping and how its LocalStack suite runs.
