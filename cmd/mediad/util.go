package main

import "os/exec"

func checkBinary(name string) error {
	_, err := exec.LookPath(name)
	return err
}
