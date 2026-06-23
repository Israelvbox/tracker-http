package main

// Implementación mínima de bencode

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
)

// ---------- DECODE ----------

// bencodeDecode decodifica bytes bencode en: string, int64, []interface{} o map[string]interface{}
func bencodeDecode(data []byte) (interface{}, error) {
	val, rest, err := decodeValue(data)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("datos sobrantes tras decodificar bencode")
	}
	return val, nil
}

func decodeValue(data []byte) (interface{}, []byte, error) {
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("bencode: datos vacíos")
	}
	switch {
	case data[0] == 'i':
		return decodeInt(data)
	case data[0] == 'l':
		return decodeList(data)
	case data[0] == 'd':
		return decodeDict(data)
	case data[0] >= '0' && data[0] <= '9':
		return decodeString(data)
	default:
		return nil, nil, fmt.Errorf("bencode: tipo desconocido %q", data[0])
	}
}

func decodeInt(data []byte) (interface{}, []byte, error) {
	end := bytes.IndexByte(data, 'e')
	if end == -1 || data[0] != 'i' {
		return nil, nil, fmt.Errorf("bencode: entero malformado")
	}
	n, err := strconv.ParseInt(string(data[1:end]), 10, 64)
	if err != nil {
		return nil, nil, err
	}
	return n, data[end+1:], nil
}

func decodeString(data []byte) (interface{}, []byte, error) {
	colon := bytes.IndexByte(data, ':')
	if colon == -1 {
		return nil, nil, fmt.Errorf("bencode: string malformado")
	}
	length, err := strconv.Atoi(string(data[:colon]))
	if err != nil || length < 0 {
		return nil, nil, fmt.Errorf("bencode: longitud de string inválida")
	}
	start := colon + 1
	end := start + length
	if end > len(data) {
		return nil, nil, fmt.Errorf("bencode: string fuera de rango")
	}
	return string(data[start:end]), data[end:], nil
}

func decodeList(data []byte) (interface{}, []byte, error) {
	rest := data[1:] // skip 'l'
	var list []interface{}
	for len(rest) > 0 && rest[0] != 'e' {
		val, newRest, err := decodeValue(rest)
		if err != nil {
			return nil, nil, err
		}
		list = append(list, val)
		rest = newRest
	}
	if len(rest) == 0 {
		return nil, nil, fmt.Errorf("bencode: lista sin terminar")
	}
	return list, rest[1:], nil
}

func decodeDict(data []byte) (interface{}, []byte, error) {
	rest := data[1:] // skip 'd'
	dict := make(map[string]interface{})
	for len(rest) > 0 && rest[0] != 'e' {
		keyVal, newRest, err := decodeString(rest)
		if err != nil {
			return nil, nil, err
		}
		key := keyVal.(string)
		val, newRest2, err := decodeValue(newRest)
		if err != nil {
			return nil, nil, err
		}
		dict[key] = val
		rest = newRest2
	}
	if len(rest) == 0 {
		return nil, nil, fmt.Errorf("bencode: diccionario sin terminar")
	}
	return dict, rest[1:], nil
}

// ---------- ENCODE ----------

// bencodeEncode codifica string, int64/int, []interface{} o map[string]interface{} a bencode.
func bencodeEncode(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeValue(buf *bytes.Buffer, v interface{}) error {
	switch val := v.(type) {
	case string:
		buf.WriteString(strconv.Itoa(len(val)))
		buf.WriteByte(':')
		buf.WriteString(val)
	case []byte:
		buf.WriteString(strconv.Itoa(len(val)))
		buf.WriteByte(':')
		buf.Write(val)
	case int:
		buf.WriteByte('i')
		buf.WriteString(strconv.Itoa(val))
		buf.WriteByte('e')
	case int64:
		buf.WriteByte('i')
		buf.WriteString(strconv.FormatInt(val, 10))
		buf.WriteByte('e')
	case []interface{}:
		buf.WriteByte('l')
		for _, item := range val {
			if err := encodeValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	case map[string]interface{}:
		buf.WriteByte('d')
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys) // bencode exige claves de diccionario ordenadas
		for _, k := range keys {
			if err := encodeValue(buf, k); err != nil {
				return err
			}
			if err := encodeValue(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	default:
		return fmt.Errorf("bencode: tipo no soportado %T", v)
	}
	return nil
}

// encodeTrackerResponse codifica directamente la respuesta del tracker (interval + peers)
func encodeTrackerResponse(interval int, peers string) []byte {
	m := map[string]interface{}{
		"interval": int64(interval),
		"peers":    peers,
	}
	out, _ := bencodeEncode(m)
	return out
}
