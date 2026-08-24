-- Initialize PostgreSQL users and database permissions
CREATE DATABASE social_mcp_db;

\c social_mcp_db;

-- Migration/Admin Role
CREATE ROLE social_mcp_admin WITH LOGIN SUPERUSER PASSWORD 'postgres_secure_local_dev';

-- Application Least-Privilege Role
CREATE ROLE social_app_user WITH LOGIN PASSWORD 'social_app_least_privilege_password';

GRANT USAGE, CREATE ON SCHEMA public TO social_mcp_admin;
