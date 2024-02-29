#!/bin/bash

morph apply up --driver postgres --dsn "postgres://postgres:postgres@localhost:5432/mattermost?sslmode=disable" --path ../server/channels/db/migrations/postgres/ --number -1
./pgloader migration.load > migration.log
