package main

import (
	// database/sql's registry is filled by these inits. The introspect
	// package takes a Querier and does not import a driver: a program
	// that only maps files would otherwise link pgx and go-mssqldb.
	// The mssql import is the root package. azuread pulls in the Azure
	// identity SDK, which this command does not use.
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
)
