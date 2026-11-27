-- Synthetic demo data. Re-running this file resets the demo database.
DROP TABLE IF EXISTS orders, customers, rdr_canary CASCADE;

CREATE TABLE customers (
  id    serial PRIMARY KEY,
  name  text NOT NULL,
  email text NOT NULL
);

CREATE TABLE orders (
  id          serial PRIMARY KEY,
  customer_id integer NOT NULL REFERENCES customers(id),
  amount      numeric(10,2) NOT NULL,
  created_at  timestamptz NOT NULL
);

-- The canary: a marker with a timestamp written by the "system" before a backup.
CREATE TABLE rdr_canary (
  id         serial PRIMARY KEY,
  written_at timestamptz NOT NULL
);

INSERT INTO customers (name, email)
SELECT 'customer ' || g, 'c' || g || '@example.test'
FROM generate_series(1, 120) AS g;

INSERT INTO orders (customer_id, amount, created_at)
SELECT 1 + (g % 120),
       round((10 + (g * 37) % 990)::numeric, 2),
       now() - (g || ' minutes')::interval
FROM generate_series(1, 600) AS g;

INSERT INTO rdr_canary (written_at) VALUES (now());
