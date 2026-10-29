-- Bootstrap options chosen in the wizard (CNI version, kube-vip, metrics-server,
-- single-node untaint...). JSON so new options do not need a migration.
ALTER TABLE clusters ADD COLUMN options TEXT NOT NULL DEFAULT '{}';
-- Address used to reach the API server from the platform (VIP or first CP).
ALTER TABLE clusters ADD COLUMN api_server_url TEXT NOT NULL DEFAULT '';
