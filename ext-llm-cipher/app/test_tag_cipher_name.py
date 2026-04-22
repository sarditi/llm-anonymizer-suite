import json, sys
import os
from app.person_model import PersonDataModel
from app.cipher_text import PersonTagger

if len(sys.argv) < 3:
    print("Usage: python person_tag_cipher_name.py <file_path> <cipher_map_path>")
    sys.exit(1)

file_path = sys.argv[1]
cipher_path = sys.argv[2]

# Read the primary text file
with open(file_path, "r", encoding="utf-8") as f:
    text = f.read()

# Check if sys.argv[2] exists
if os.path.exists(cipher_path):
    with open(cipher_path, "r", encoding="utf-8") as f:
        cipher_map_string = f.read()
else:
    cipher_map_string = ""

data_model = PersonDataModel.from_json_string(cipher_map_string)

tagger_instance = PersonTagger()
is_data_model_changed, ann_text = tagger_instance.tag_file_persons(text, data_model, 1, "the seed")

with open("../anonymized_output.txt", "w") as f:
    f.write(ann_text)

print("===== Chat decipher =====")
print(json.dumps(data_model.chat_decipher,indent=4))

print("===== cipher_map output =====")
print(data_model.model_dump_json(indent=4))

os._exit(0)