# Security policy

Codr holds people's source code, so security reports come first.

## Reporting a vulnerability

Please do not open a public issue. Email **admin@sasateguh.com** with:

- what you found and where (version, page, command or endpoint),
- the steps to reproduce it, and
- what an attacker could do with it.

You will get a reply within three working days. Once a fix is released, you are credited in the release notes
unless you ask not to be.

## Supported versions

Only the latest release receives security fixes. Upgrading is one binary: stop `codr serve`, replace the binary,
run `codr migrate`, start it again.
