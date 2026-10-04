import os
import glob
import yaml

def patch_yaml(filepath):
    with open(filepath, 'r') as f:
        docs = list(yaml.safe_load_all(f))
    
    modified = False
    for doc in docs:
        if not doc: continue
        if doc.get('kind') == 'XAccessPolicy':
            if doc.get('apiVersion') == 'agentic.agentic.networking.x-k8s.io/v1alpha1':
                doc['apiVersion'] = 'agentic.networking.x-k8s.io/v1alpha1'
                modified = True
            
            if 'spec' in doc:
                if 'action' not in doc['spec']:
                    doc['spec']['action'] = 'Allow'
                    modified = True
                
                if 'rules' in doc['spec']:
                    for rule in doc['spec']['rules']:
                        if 'source' not in rule:
                            rule['source'] = {
                                'type': 'ServiceAccount',
                                'serviceAccount': {'name': 'default'}
                            }
                            modified = True
    
    if modified:
        with open(filepath, 'w') as f:
            yaml.dump_all(docs, f, sort_keys=False)
        print(f"Patched {filepath}")

for path in glob.glob('quickstart/policy/*.yaml') + glob.glob('demo-multi/policy/*.yaml'):
    patch_yaml(path)
