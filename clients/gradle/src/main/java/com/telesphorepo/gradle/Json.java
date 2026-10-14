package com.telesphorepo.gradle;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * A small JSON reader and writer for the server's API. Objects read as {@code Map<String, Object>},
 * arrays as {@code List<Object>}, integers as {@code Long}, other numbers as {@code Double}.
 */
final class Json {
    private final String s;
    private int i;

    private Json(String s) {
        this.s = s;
    }

    static Object parse(String text) {
        Json p = new Json(text);
        p.ws();
        Object v = p.value();
        p.ws();
        if (p.i != p.s.length()) {
            throw p.error("trailing data");
        }
        return v;
    }

    @SuppressWarnings("unchecked")
    static Map<String, Object> parseObject(String text) {
        Object v = parse(text);
        if (!(v instanceof Map)) {
            throw new IllegalArgumentException("JSON: expected an object");
        }
        return (Map<String, Object>) v;
    }

    static String write(Map<String, ?> m) {
        StringBuilder b = new StringBuilder();
        writeValue(b, m);
        return b.toString();
    }

    /** Returns the string field k, or null when it is absent or not a string. */
    static String string(Map<String, Object> m, String k) {
        Object v = m.get(k);
        return v instanceof String ? (String) v : null;
    }

    /** Returns field k as text: a string as is, a number in decimal, otherwise null. */
    static String text(Map<String, Object> m, String k) {
        Object v = m.get(k);
        if (v instanceof String || v instanceof Long) {
            return v.toString();
        }
        if (v instanceof Double) {
            double d = (Double) v;
            return d == Math.rint(d) ? Long.toString((long) d) : v.toString();
        }
        return null;
    }

    static long number(Map<String, Object> m, String k, long fallback) {
        Object v = m.get(k);
        return v instanceof Number ? ((Number) v).longValue() : fallback;
    }

    private static void writeValue(StringBuilder b, Object v) {
        if (v == null) {
            b.append("null");
        } else if (v instanceof String) {
            writeString(b, (String) v);
        } else if (v instanceof Number || v instanceof Boolean) {
            b.append(v);
        } else if (v instanceof Map) {
            b.append('{');
            boolean first = true;
            for (Map.Entry<?, ?> e : ((Map<?, ?>) v).entrySet()) {
                if (!first) {
                    b.append(',');
                }
                first = false;
                writeString(b, e.getKey().toString());
                b.append(':');
                writeValue(b, e.getValue());
            }
            b.append('}');
        } else if (v instanceof List) {
            b.append('[');
            boolean first = true;
            for (Object o : (List<?>) v) {
                if (!first) {
                    b.append(',');
                }
                first = false;
                writeValue(b, o);
            }
            b.append(']');
        } else {
            throw new IllegalArgumentException("JSON: cannot write " + v.getClass().getName());
        }
    }

    private static void writeString(StringBuilder b, String s) {
        b.append('"');
        for (int k = 0; k < s.length(); k++) {
            char c = s.charAt(k);
            switch (c) {
                case '"': b.append("\\\""); break;
                case '\\': b.append("\\\\"); break;
                case '\n': b.append("\\n"); break;
                case '\r': b.append("\\r"); break;
                case '\t': b.append("\\t"); break;
                default:
                    if (c < 0x20) {
                        b.append(String.format("\\u%04x", (int) c));
                    } else {
                        b.append(c);
                    }
            }
        }
        b.append('"');
    }

    private Object value() {
        if (i >= s.length()) {
            throw error("unexpected end");
        }
        char c = s.charAt(i);
        switch (c) {
            case '{': return object();
            case '[': return array();
            case '"': return string();
            case 't': return literal("true", Boolean.TRUE);
            case 'f': return literal("false", Boolean.FALSE);
            case 'n': return literal("null", null);
            default: return number();
        }
    }

    private Map<String, Object> object() {
        Map<String, Object> m = new LinkedHashMap<>();
        i++;
        ws();
        if (peek('}')) {
            i++;
            return m;
        }
        while (true) {
            ws();
            if (!peek('"')) {
                throw error("expected a key");
            }
            String k = string();
            ws();
            expect(':');
            ws();
            m.put(k, value());
            ws();
            if (peek(',')) {
                i++;
            } else {
                expect('}');
                return m;
            }
        }
    }

    private List<Object> array() {
        List<Object> l = new ArrayList<>();
        i++;
        ws();
        if (peek(']')) {
            i++;
            return l;
        }
        while (true) {
            ws();
            l.add(value());
            ws();
            if (peek(',')) {
                i++;
            } else {
                expect(']');
                return l;
            }
        }
    }

    private String string() {
        i++;
        StringBuilder b = new StringBuilder();
        while (true) {
            if (i >= s.length()) {
                throw error("unterminated string");
            }
            char c = s.charAt(i++);
            if (c == '"') {
                return b.toString();
            }
            if (c != '\\') {
                b.append(c);
                continue;
            }
            if (i >= s.length()) {
                throw error("unterminated escape");
            }
            char e = s.charAt(i++);
            switch (e) {
                case '"': b.append('"'); break;
                case '\\': b.append('\\'); break;
                case '/': b.append('/'); break;
                case 'b': b.append('\b'); break;
                case 'f': b.append('\f'); break;
                case 'n': b.append('\n'); break;
                case 'r': b.append('\r'); break;
                case 't': b.append('\t'); break;
                case 'u':
                    if (i + 4 > s.length()) {
                        throw error("short unicode escape");
                    }
                    b.append((char) Integer.parseInt(s.substring(i, i + 4), 16));
                    i += 4;
                    break;
                default:
                    throw error("bad escape");
            }
        }
    }

    private Object number() {
        int start = i;
        boolean integer = true;
        while (i < s.length()) {
            char c = s.charAt(i);
            if (c >= '0' && c <= '9' || c == '-' || c == '+') {
                i++;
            } else if (c == '.' || c == 'e' || c == 'E') {
                integer = false;
                i++;
            } else {
                break;
            }
        }
        String n = s.substring(start, i);
        if (n.isEmpty()) {
            throw error("unexpected character");
        }
        try {
            return integer ? (Object) Long.parseLong(n) : (Object) Double.parseDouble(n);
        } catch (NumberFormatException e) {
            throw error("bad number");
        }
    }

    private Object literal(String word, Object v) {
        if (!s.startsWith(word, i)) {
            throw error("unexpected character");
        }
        i += word.length();
        return v;
    }

    private void ws() {
        while (i < s.length() && Character.isWhitespace(s.charAt(i))) {
            i++;
        }
    }

    private boolean peek(char c) {
        return i < s.length() && s.charAt(i) == c;
    }

    private void expect(char c) {
        if (!peek(c)) {
            throw error("expected '" + c + "'");
        }
        i++;
    }

    private IllegalArgumentException error(String msg) {
        return new IllegalArgumentException("JSON: " + msg + " at offset " + i);
    }
}
