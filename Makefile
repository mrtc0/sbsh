.PHONY: python-wasm
# Rebuilds the committed Python runtime in pywasm/dist. Only maintainers need
# this: building the repository does not, because the artifacts are committed.
#
# The export goes to a fresh directory and is swapped in afterwards. A local
# export writes over what is already there without removing anything, so a file
# that upstream dropped would linger in the committed tree; and swapping last
# means a failed build leaves pywasm/dist as it was.
python-wasm:
	rm -rf pywasm/dist.tmp
	docker build -f pywasm/Dockerfile --target artifacts \
	  --output type=local,dest=pywasm/dist.tmp pywasm
	rm -rf pywasm/dist
	mv pywasm/dist.tmp pywasm/dist
