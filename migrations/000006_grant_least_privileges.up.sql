-- Ensure application runtime role exists with least privileges
DO
$do$
BEGIN
   IF NOT EXISTS (
      SELECT FROM pg_catalog.pg_roles
      WHERE  rolname = 'social_app_user') THEN

      CREATE ROLE social_app_user WITH LOGIN PASSWORD 'social_app_least_privilege_password';
   END IF;
END
$do$;

-- Grant standard DML access only
GRANT USAGE ON SCHEMA public TO social_app_user;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO social_app_user;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO social_app_user;

-- Set default privileges for any future tables created by migrations
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO social_app_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO social_app_user;
