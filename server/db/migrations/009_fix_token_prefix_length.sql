-- Fix: token_prefix column too short for roost_{8chars} format (14 chars needed)
ALTER TABLE api_tokens ALTER COLUMN token_prefix TYPE VARCHAR(20);
