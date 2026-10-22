import { ProFormDatePicker, ProFormDigit, ProFormGroup, ProFormSelect, ProFormSwitch, ProFormText, ProFormTextArea } from '../pro';
import type { JsonSchema } from '../api/types';

function typeOf(s: JsonSchema): string {
  return Array.isArray(s.type) ? (s.type.find((x) => x !== 'null') ?? 'string') : (s.type ?? (s.properties ? 'object' : 'string'));
}

/** Renders ProForm fields for a (subset of) JSON schema. Used for connector config and the form designer preview. */
export function JsonSchemaFields({ schema, name = [] }: { schema?: JsonSchema | null; name?: (string | number)[] }) {
  if (!schema?.properties) return null;
  const required = new Set(schema.required ?? []);
  return (
    <>
      {Object.entries(schema.properties).map(([key, prop]) => {
        const path = [...name, key];
        const label = prop.title ?? key;
        const rules = required.has(key) ? [{ required: true }] : undefined;
        const common = { key, name: path, label, rules, tooltip: prop.description, initialValue: prop.default as never };
        const type = typeOf(prop);
        if (prop.enum) {
          return <ProFormSelect {...common} options={prop.enum.map((v) => ({ value: v, label: String(v) }))} />;
        }
        switch (type) {
          case 'integer':
          case 'number':
            return <ProFormDigit {...common} fieldProps={{ precision: type === 'integer' ? 0 : undefined }} />;
          case 'boolean':
            return <ProFormSwitch {...common} rules={undefined} />;
          case 'array':
            return <ProFormSelect {...common} mode="tags" />;
          case 'object':
            return (
              <ProFormGroup key={key} title={label}>
                <JsonSchemaFields schema={prop} name={path} />
              </ProFormGroup>
            );
          default:
            if (prop.format === 'password' || /secret|password|token/i.test(key)) return <ProFormText.Password {...common} />;
            if (prop.format === 'date') return <ProFormDatePicker {...common} />;
            if (prop.format === 'textarea' || prop.format === 'pem') return <ProFormTextArea {...common} />;
            return <ProFormText {...common} />;
        }
      })}
    </>
  );
}
