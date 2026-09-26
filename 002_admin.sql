CREATE TABLE IF NOT EXISTS admins (
    id integer PRIMARY KEY CHECK (id = 1),
    username text NOT NULL CHECK (length(username) BETWEEN 1 AND 200),
    password_hash text NOT NULL
);
