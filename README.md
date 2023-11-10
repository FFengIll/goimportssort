# go-imports-sort
This tool aims to automatically fix the order of golang imports. 

This repo is a fork of original [goimportssort](https://github.com/AanZee/goimportssort) with perf and more features.

# Install
```sh
go install github.com/FFengIll/sortimport@latest
```

# Original Features
- Automatically split your imports in three categories: inbuilt, external and local.
- Written fully in Golang, no dependencies, works on any platform.
- Detects Go module name automatically.
- Orders your imports alphabetically.
- Removes additional line breaks.
- No more manually fixing import orders.

# New Features
- Load standard go module only once for all task. (complete more quick).
- Support secondary package prefix (2-part-package) which will sort import into 4 groups.
- Cache standard package info to reduce parse time cost and run more quickly.
