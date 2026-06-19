package buildinfo

// Version is stamped by Makefile targets with -ldflags. It stays "dev" when
// a binary is run without the project Makefile.
var Version = "dev"
