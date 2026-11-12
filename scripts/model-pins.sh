# Pins of the semantic search model (ADR-019), sourced by
# render-model-cask.sh and the model-release workflow. The file is the
# llama.cpp project's GGUF conversion of BAAI/bge-m3 (MIT), republished as an
# asset of this repo's release MODEL_TAG so installs depend only on GitHub.
#
# Changing the model: update SOURCE_URL, FILE and SHA256 (and embed.ModelFile),
# bump MODEL_VERSION, run the "Model release" workflow, then release the app.
MODEL_VERSION="1"
MODEL_TAG="model-bge-m3-q8_0-v${MODEL_VERSION}"
FILE="bge-m3-q8_0.gguf"
SHA256="aa473d51f451a22f0fcf39ba3330c14bed38a385712b1113440f69df4047a173"
SOURCE_URL="https://huggingface.co/ggml-org/bge-m3-Q8_0-GGUF/resolve/9eba04c5d75ba5a1595e45de734d36bef4e5cb98/bge-m3-q8_0.gguf"
