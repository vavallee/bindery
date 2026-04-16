-- Add support for Transmission torrent client
ALTER TABLE downloads ADD COLUMN torrent_id TEXT;
CREATE INDEX idx_downloads_torrent_id ON downloads(torrent_id);
