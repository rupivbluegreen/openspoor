-- The application connects as a non-superuser so row-level security is enforced.
create role openspoor login password 'openspoor' createdb;
create database openspoor owner openspoor;
create role keycloak login password 'keycloak';
create database keycloak owner keycloak;
create role temporal login password 'temporal' createdb;
