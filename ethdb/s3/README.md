ethdb/s3
========

## Integration testing

To run integration tests, specify the `integration` tag during tests and pass
in the appropriate settings for your S3-compatible bucket:

```sh
$ go test -tags integration . -endpoint nyc3.digitaloceanspaces.com -bucket gochain-test -access-key-id 00000000000000000000 -secret-access-key 0000000/00000000000000000000000000000000000
```

Files on the bucket will be automatically cleaned up after a successful test run.