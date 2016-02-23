// +build darwin

package main

func devNo(major, minor int64) int { return int((major << 24) + minor) }
