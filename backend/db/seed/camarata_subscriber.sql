-- X0-T15: Camarata Subscriber Seed (Roost)
-- Idempotent: ON CONFLICT DO NOTHING

BEGIN;

-- Step 1: Insert Camarata subscriber (Ali — billing exempt, superowner, founder)
-- Password hash: bcrypt of 'camarata-dev-placeholder-change-on-first-login'
-- Using a placeholder hash since we can't bcrypt in SQL
INSERT INTO subscribers (
  id, email, password_hash, display_name,
  email_verified, status, is_superowner, billing_exempt
) VALUES (
  'b0000001-0000-0000-0000-000000000001',
  'alisalaah@gmail.com',
  '$2a$12$placeholder.hash.change.on.first.password.reset.needed',
  'Ali Camarata',
  true,
  'active',
  true,
  true
) ON CONFLICT (email) DO NOTHING;

-- Step 2: Link to Founder plan (id from seed: 9881eca1-3077-4301-98dd-1edd2c6c9a4f)
INSERT INTO subscriptions (
  subscriber_id, plan_id, status,
  current_period_start, current_period_end
) VALUES (
  'b0000001-0000-0000-0000-000000000001',
  '9881eca1-3077-4301-98dd-1edd2c6c9a4f',
  'active',
  now(),
  now() + interval '100 years'
) ON CONFLICT DO NOTHING;

COMMIT;
