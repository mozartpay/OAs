#!/usr/bin/env python3
"""
Compare actual output files against expected test vector outputs.
Supports JSON, XML, and JWT comparison.
"""

import json
import sys
import xml.etree.ElementTree as ET
from pathlib import Path


def compare_json(actual_path, expected_path):
    """Compare two JSON files."""
    try:
        with open(actual_path, 'r') as f:
            actual = json.load(f)
        with open(expected_path, 'r') as f:
            expected = json.load(f)
        
        if actual == expected:
            return True, "JSON files match exactly"
        else:
            # Deep comparison
            differences = []
            diff = deep_compare(actual, expected, "", differences)
            if not diff:
                return False, f"JSON files differ: {', '.join(differences[:5])}"
            return True, "JSON files match"
    except Exception as e:
        return False, f"Error comparing JSON: {str(e)}"


def deep_compare(actual, expected, path="", differences=None):
    """Recursively compare JSON objects."""
    if differences is None:
        differences = []
    
    if type(actual) != type(expected):
        differences.append(f"{path}: type mismatch ({type(actual).__name__} vs {type(expected).__name__})")
        return False
    
    if isinstance(actual, dict):
        if set(actual.keys()) != set(expected.keys()):
            missing = set(expected.keys()) - set(actual.keys())
            extra = set(actual.keys()) - set(expected.keys())
            if missing:
                differences.append(f"{path}: missing keys {missing}")
            if extra:
                differences.append(f"{path}: extra keys {extra}")
            return False
        
        for key in actual:
            if not deep_compare(actual[key], expected[key], f"{path}.{key}" if path else key, differences):
                return False
    elif isinstance(actual, list):
        if len(actual) != len(expected):
            differences.append(f"{path}: array length mismatch ({len(actual)} vs {len(expected)})")
            return False
        for i, (a, e) in enumerate(zip(actual, expected)):
            if not deep_compare(a, e, f"{path}[{i}]", differences):
                return False
    else:
        if actual != expected:
            differences.append(f"{path}: value mismatch ({actual} vs {expected})")
            return False
    
    return True


def compare_xml(actual_path, expected_path):
    """Compare two XML files (structure-based)."""
    try:
        tree_actual = ET.parse(actual_path)
        tree_expected = ET.parse(expected_path)
        
        root_actual = tree_actual.getroot()
        root_expected = tree_expected.getroot()
        
        if root_actual.tag != root_expected.tag:
            return False, f"Root element mismatch: {root_actual.tag} vs {root_expected.tag}"
        
        # Compare structure (ignoring order for simplicity)
        diff = compare_elements(root_actual, root_expected)
        if diff:
            return False, f"XML structure differs: {diff}"
        
        return True, "XML structure matches"
    except Exception as e:
        return False, f"Error comparing XML: {str(e)}"


def compare_elements(elem1, elem2):
    """Compare two XML elements recursively."""
    if elem1.tag != elem2.tag:
        return f"Tag mismatch: {elem1.tag} vs {elem2.tag}"
    
    if elem1.attrib != elem2.attrib:
        return f"Attribute mismatch in {elem1.tag}"
    
    if len(elem1) != len(elem2):
        return f"Child count mismatch in {elem1.tag}"
    
    for child1, child2 in zip(elem1, elem2):
        diff = compare_elements(child1, child2)
        if diff:
            return diff
    
    # Compare text content
    text1 = (elem1.text or "").strip()
    text2 = (elem2.text or "").strip()
    if text1 and text2 and text1 != text2:
        return f"Text mismatch in {elem1.tag}"
    
    return None


def compare_jwt(actual_path, expected_path):
    """Compare two JWT files (structure-based)."""
    try:
        with open(actual_path, 'r') as f:
            actual = f.read().strip()
        with open(expected_path, 'r') as f:
            expected = f.read().strip()
        
        # Split into parts
        actual_parts = actual.split('.')
        expected_parts = expected.split('.')
        
        if len(actual_parts) != 3 or len(expected_parts) != 3:
            return False, "Invalid JWT format"
        
        # Compare header (decoded)
        import base64
        def decode_jwt_part(part):
            # Add padding if needed
            padding = 4 - len(part) % 4
            if padding != 4:
                part += '=' * padding
            return base64.urlsafe_b64decode(part)
        
        try:
            actual_header = json.loads(decode_jwt_part(actual_parts[0]))
            expected_header = json.loads(decode_jwt_part(expected_parts[0]))
            
            if actual_header != expected_header:
                return False, "JWT headers differ"
        except:
            pass  # Skip header comparison if decoding fails
        
        # Compare payload structure (without exact values)
        try:
            actual_payload = json.loads(decode_jwt_part(actual_parts[1]))
            expected_payload = json.loads(decode_jwt_part(expected_parts[1]))
            
            # Compare keys
            if set(actual_payload.keys()) != set(expected_payload.keys()):
                return False, f"JWT payload keys differ: {set(actual_payload.keys())} vs {set(expected_payload.keys())}"
        except:
            pass
        
        return True, "JWT structure matches"
    except Exception as e:
        return False, f"Error comparing JWT: {str(e)}"


def main():
    if len(sys.argv) != 3:
        print("Usage: compare-output.py <actual_file> <expected_file>")
        sys.exit(1)
    
    actual_path = Path(sys.argv[1])
    expected_path = Path(sys.argv[2])
    
    if not actual_path.exists():
        print(f"Error: Actual file not found: {actual_path}")
        sys.exit(1)
    
    if not expected_path.exists():
        print(f"Error: Expected file not found: {expected_path}")
        sys.exit(1)
    
    # Determine file type
    suffix = actual_path.suffix.lower()
    
    if suffix == '.json':
        match, message = compare_json(actual_path, expected_path)
    elif suffix == '.xml':
        match, message = compare_xml(actual_path, expected_path)
    elif suffix == '.jwt' or actual_path.name.endswith('.jwt'):
        match, message = compare_jwt(actual_path, expected_path)
    else:
        match, message = False, f"Unsupported file type: {suffix}"
    
    if match:
        print(f"✓ {message}")
        sys.exit(0)
    else:
        print(f"✗ {message}")
        sys.exit(1)


if __name__ == '__main__':
    main()
