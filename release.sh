#!/bin/bash
set -exuo pipefail

user="gochain"
image="gochain"
gcr_project="gochain-core"
# ensure working dir is clean
git status
if [[ -z $(git status -s) ]]
then
  echo "tree is clean"
else
  echo "tree is dirty, please commit changes before running this"
  exit 1
fi

version_file="params/version.go"
docker create -v /params --name file alpine /bin/true
docker cp $version_file file:/$version_file
# Bump version, patch by default - also checks if previous commit message contains `[bump X]`, and if so, bumps the appropriate semver number - https://github.com/treeder/dockers/tree/master/bump
docker run --rm -it --volumes-from file -w / treeder/bump --filename $version_file "$(git log -1 --pretty=%B)"
docker cp file:/$version_file $version_file
version=$(grep -m1 -Eo "[0-9]+\.[0-9]+\.[0-9]+" $version_file)
echo "Version: $version"

docker build . -t $user/$image
git add -u
git commit -m "$image: $version release [skip ci]"
git tag -f -a "v$version" -m "version $version"
git push
git push origin v$version

# Finally, push docker images
docker tag $user/$image:latest $user/$image:$version
docker push $user/$image:$version
docker push $user/$image:latest

#Push docker images to GCR
./tmp/google-cloud-sdk/bin/gcloud auth activate-service-account --key-file=${HOME}/gcloud-service-key.json
docker tag $user/$image:latest gcr.io/$gcr_project/$image:latest
docker tag $user/$image:latest gcr.io/$gcr_project/$image:$version
docker push gcr.io/$gcr_project/$image:latest
docker push gcr.io/$gcr_project/$image:$version
