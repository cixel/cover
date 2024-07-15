Coverage instrumentation program created for lightning talk at GopherCon 2024: [github.com/cixel/gc2024](https://github.com/cixel/gc2024)

This is just a learning tool and should not be used seriously.
If you need code coverage, please use the [tool built into Go](https://go.dev/doc/build-cover).

## Usage

```
$ go install ehden.net/cover
$ go build -toolexec cover
```

The `COVER_PATHS` environment variable can contain a comma-separated list of packages which should be instrumented.
If missing or empty, only the `main` package is instrumented.

If `COVER_PATHS=*`, all packages in the build will be instrumented.
This is rather expensive and isn't recommended, but could be fun.
