-- 0010: the product is named Codr (D58). The display name is a setting (D6): an install still showing the
-- old default becomes Codr; a name an operator chose is left alone.
UPDATE settings SET value = 'Codr', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE key = 'site.name' AND value = 'AMCD';
