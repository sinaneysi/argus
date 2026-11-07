//go:build !demo

package main

import _ "time/tzdata" // TZ=Europe/Amsterdam in a scratch image (the demo build runs in UTC and leaves it out)
