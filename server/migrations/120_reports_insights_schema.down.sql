DROP TRIGGER IF EXISTS report_execution_immutable ON report_execution;
DROP FUNCTION IF EXISTS prevent_report_execution_update();

DROP TABLE IF EXISTS report_execution;
DROP TABLE IF EXISTS report;
DROP TABLE IF EXISTS dashboard_tile;
DROP TABLE IF EXISTS dashboard;
DROP TABLE IF EXISTS insight;
