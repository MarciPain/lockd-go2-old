#!/bin/bash
go build -o lockd2 .
install -o root -g root -m 0755 lockd2 /usr/local/bin/lockd2
service lockd2 restart