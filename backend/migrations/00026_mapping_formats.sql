-- +goose Up
-- Mapping studio: X12, EDIFACT and generic XML sample files can be mapped, so the format check
-- of a saved mapping grows by three values.
alter table b2b_mapping drop constraint if exists b2b_mapping_format_check;
alter table b2b_mapping add constraint b2b_mapping_format_check
  check (format in ('csv','json','flat','xml','x12','edifact'));

-- +goose Down
-- Mappings in the new formats cannot be kept under the old check.
delete from b2b_mapping where format in ('xml','x12','edifact');
alter table b2b_mapping drop constraint b2b_mapping_format_check;
alter table b2b_mapping add constraint b2b_mapping_format_check check (format in ('csv','json','flat'));
