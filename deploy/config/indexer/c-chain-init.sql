-- Creates the indexer database on the first start of a <chain>-indexer-db service
-- (MySQL runs everything under /docker-entrypoint-initdb.d once).
CREATE DATABASE IF NOT EXISTS `db`;
