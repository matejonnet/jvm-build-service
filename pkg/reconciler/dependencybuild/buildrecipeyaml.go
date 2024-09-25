package dependencybuild

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"github.com/go-logr/logr"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/artifactbuild"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/jbsconfig"
	tektonpipeline "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	WorkspaceBuildSettings = "build-settings"
	WorkspaceSource        = "source"
	WorkspaceMount         = "/var/workdir"
	WorkspaceTls           = "tls"

	PreBuildTaskName  = "pre-build"
	BuildTaskName     = "build"
	PostBuildTaskName = "post-build"
	TagTaskName       = "tag"
)

//go:embed scripts/maven-build.sh
var mavenBuild string

// used for both ant and maven
//
//go:embed scripts/maven-settings.sh
var mavenSettings string

//go:embed scripts/gradle-build.sh
var gradleBuild string

//go:embed scripts/sbt-build.sh
var sbtBuild string

//go:embed scripts/ant-build.sh
var antBuild string

//go:embed scripts/install-package.sh
var packageTemplate string

//go:embed scripts/dockerfile-entry-script.sh
var dockerfileEntryScript string

//go:embed scripts/build-entry.sh
var buildEntryScript string

//go:embed scripts/Dockerfile.build-trusted-artifacts
var buildTrustedArtifacts string

func createDeployPipelineSpec(jbsConfig *v1alpha1.JBSConfig, buildRequestProcessorImage string, gavs string) (*tektonpipeline.PipelineSpec, error) {
	zero := int64(0)
	mavenDeployArgs := pipelineDeployCommands(jbsConfig)

	limits, err := memoryLimits(jbsConfig, 0)
	if err != nil {
		return nil, err
	}
	orasOptions := ""
	if jbsConfig.Annotations != nil && jbsConfig.Annotations[jbsconfig.TestRegistry] == "true" {
		orasOptions = "--insecure --plain-http"
	}

	secretVariables := secretVariables(jbsConfig)
	pullPolicy := pullPolicy(buildRequestProcessorImage)
	regUrl := registryArgsWithDefaults(jbsConfig, "")

	tagTask := tektonpipeline.TaskSpec{
		Workspaces: []tektonpipeline.WorkspaceDeclaration{{Name: WorkspaceTls}, {Name: WorkspaceSource, MountPath: WorkspaceMount}},
		Params: []tektonpipeline.ParamSpec{
			{Name: PipelineResultPreBuildImageDigest, Type: tektonpipeline.ParamTypeString},
			{Name: PipelineResultImageDigest, Type: tektonpipeline.ParamTypeString},
			{Name: PipelineResultImage, Type: tektonpipeline.ParamTypeString}},
		Steps: []tektonpipeline.Step{
			{
				Name:            "restore-post-build-artifacts",
				Image:           strings.TrimSpace(strings.Split(buildTrustedArtifacts, "FROM")[1]),
				ImagePullPolicy: v1.PullIfNotPresent,
				SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
				Env:             secretVariables,
				// While the manifest digest is available we need the manifest of the layer within
				// the archive hence using 'oras manifest fetch' to extract the correct layer.
				Script: fmt.Sprintf(`echo "Restoring artifacts and source to workspace"
export ORAS_OPTIONS="%s"
use-archive $(params.%s)=$(workspaces.source.path)/source
mv $(workspaces.source.path)/source/.jbs/build.sh $(workspaces.source.path)
URL=$(params.%s)
DIGEST=$(params.%s)
AARCHIVE=$(oras manifest fetch $ORAS_OPTIONS $URL@$DIGEST | jq --raw-output '.layers[0].digest')
echo "URL $URL DIGEST $DIGEST AARCHIVE $AARCHIVE"
use-archive oci:$URL@$AARCHIVE=$(workspaces.source.path)/artifacts`, orasOptions, PipelineResultPreBuildImageDigest, PipelineResultImage, PipelineResultImageDigest),
			},
			{
				Name:            "maven-deployment",
				Image:           buildRequestProcessorImage,
				ImagePullPolicy: pullPolicy,
				SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
				Env:             secretVariables,
				ComputeResources: v1.ResourceRequirements{
					Requests: v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultRequestCPU},
					Limits:   v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultLimitCPU},
				},
				Script: artifactbuild.InstallKeystoreIntoBuildRequestProcessor(mavenDeployArgs),
			},
			{
				Name:            "oras-tag",
				Image:           strings.TrimSpace(strings.Split(buildTrustedArtifacts, "FROM")[1]),
				ImagePullPolicy: v1.PullIfNotPresent,
				SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
				Env:             secretVariables,
				// gavs is a comma separated list so split it into spaces
				Script: fmt.Sprintf(`GAVS=%s
echo "Tagging for GAVs ($GAVS)"
oras tag %s --verbose %s@$(params.%s) ${GAVS//,/ }`, gavs, orasOptions, regUrl, PipelineResultImageDigest),
			},
		},
	}

	ps := &tektonpipeline.PipelineSpec{
		Params: []tektonpipeline.ParamSpec{{Name: PipelineResultImageDigest, Type: tektonpipeline.ParamTypeString}},
		Tasks: []tektonpipeline.PipelineTask{
			{
				Name: TagTaskName,
				TaskSpec: &tektonpipeline.EmbeddedTask{
					TaskSpec: tagTask,
				},
				Params: []tektonpipeline.Param{
					{Name: PipelineResultImage, Value: tektonpipeline.ParamValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(params." + PipelineResultImage + ")"}},
					{Name: PipelineResultImageDigest, Value: tektonpipeline.ParamValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(params." + PipelineResultImageDigest + ")"}},
					{Name: PipelineResultPreBuildImageDigest, Value: tektonpipeline.ParamValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(params." + PipelineResultPreBuildImageDigest + ")"}},
				},
				Workspaces: []tektonpipeline.WorkspacePipelineTaskBinding{
					{Name: WorkspaceTls, Workspace: WorkspaceTls},
					{Name: WorkspaceSource, Workspace: WorkspaceSource},
				},
			},
		},
		Workspaces: []tektonpipeline.PipelineWorkspaceDeclaration{{Name: WorkspaceSource}, {Name: WorkspaceTls}},
	}
	return ps, nil
}
func createPipelineSpec(log logr.Logger, tool string, commitTime int64, jbsConfig *v1alpha1.JBSConfig, systemConfig *v1alpha1.SystemConfig, recipe *v1alpha1.BuildRecipe, db *v1alpha1.DependencyBuild, paramValues []tektonpipeline.Param, buildRequestProcessorImage string, buildId string, existingImages map[string]string) (*tektonpipeline.PipelineSpec, string, string, string, error) {

	// Rather than tagging with hash of json build recipe, buildrequestprocessor image and db.Name as the former two
	// could change with new image versions just use db.Name (which is a hash of scm url/tag/path so should be stable)
	imageId := db.Name
	zero := int64(0)
	verifyBuiltArtifactsArgs := verifyParameters(jbsConfig, recipe)
	preBuildImageArgs, copyArtifactsArgs, deployArgs, konfluxArgs := pipelineBuildCommands(imageId, db, jbsConfig, buildId)

	gitScript := gitScript(db, recipe)
	install := additionalPackages(recipe)
	orasOptions := ""
	tlsVerify := "true"
	if jbsConfig.Annotations != nil && jbsConfig.Annotations[jbsconfig.TestRegistry] == "true" {
		orasOptions = "--insecure --plain-http"
		tlsVerify = "false"
	}

	preprocessorArgs := []string{
		"maven-prepare",
		"$(workspaces." + WorkspaceSource + ".path)/source",
	}
	if len(recipe.DisabledPlugins) > 0 {
		for _, i := range recipe.DisabledPlugins {
			preprocessorArgs = append(preprocessorArgs, "-dp "+i)
		}
	}
	var javaHome string
	if recipe.JavaVersion == "7" || recipe.JavaVersion == "8" {
		javaHome = "/lib/jvm/java-1." + recipe.JavaVersion + ".0"
	} else {
		javaHome = "/lib/jvm/java-" + recipe.JavaVersion
	}

	toolEnv := []v1.EnvVar{}
	if recipe.ToolVersions["maven"] != "" {
		toolEnv = append(toolEnv, v1.EnvVar{Name: "MAVEN_HOME", Value: "/opt/maven/" + recipe.ToolVersions["maven"]})
	}
	if recipe.ToolVersions["gradle"] != "" {
		toolEnv = append(toolEnv, v1.EnvVar{Name: "GRADLE_HOME", Value: "/opt/gradle/" + recipe.ToolVersions["gradle"]})
	}
	if recipe.ToolVersions["ant"] != "" {
		toolEnv = append(toolEnv, v1.EnvVar{Name: "ANT_HOME", Value: "/opt/ant/" + recipe.ToolVersions["ant"]})
	}
	if recipe.ToolVersions["sbt"] != "" {
		toolEnv = append(toolEnv, v1.EnvVar{Name: "SBT_DIST", Value: "/opt/sbt/" + recipe.ToolVersions["sbt"]})
	}
	toolEnv = append(toolEnv, v1.EnvVar{Name: PipelineParamToolVersion, Value: recipe.ToolVersion})
	toolEnv = append(toolEnv, v1.EnvVar{Name: PipelineParamProjectVersion, Value: db.Spec.Version})
	toolEnv = append(toolEnv, v1.EnvVar{Name: JavaHome, Value: javaHome})
	toolEnv = append(toolEnv, v1.EnvVar{Name: PipelineParamEnforceVersion, Value: recipe.EnforceVersion})

	additionalMemory := recipe.AdditionalMemory
	if systemConfig.Spec.MaxAdditionalMemory > 0 && additionalMemory > systemConfig.Spec.MaxAdditionalMemory {
		log.Info(fmt.Sprintf("additionalMemory specified %#v but system MaxAdditionalMemory is %#v and is limiting that value", additionalMemory, systemConfig.Spec.MaxAdditionalMemory))
		additionalMemory = systemConfig.Spec.MaxAdditionalMemory
	}
	var buildToolSection string
	trueBool := true
	if tool == "maven" {
		buildToolSection = mavenSettings + "\n" + mavenBuild
	} else if tool == "gradle" {
		// We always add Maven information (in InvocationBuilder) so add the relevant settings.xml
		buildToolSection = mavenSettings + "\n" + gradleBuild
		preprocessorArgs = []string{
			"gradle-prepare",
			"$(workspaces." + WorkspaceSource + ".path)/source",
		}
		if len(recipe.DisabledPlugins) > 0 {
			for _, i := range recipe.DisabledPlugins {
				preprocessorArgs = append(preprocessorArgs, "-dp "+i)
			}
		}
	} else if tool == "sbt" {
		buildToolSection = sbtBuild
		preprocessorArgs[0] = "sbt-prepare"
	} else if tool == "ant" {
		// We always add Maven information (in InvocationBuilder) so add the relevant settings.xml
		buildToolSection = mavenSettings + "\n" + antBuild
		preprocessorArgs[0] = "ant-prepare"
	} else {
		buildToolSection = "echo unknown build tool " + tool + " && exit 1"
	}
	build := buildEntryScript
	//horrible hack
	//we need to get our TLS CA's into our trust store
	//we just add it at the start of the build
	build = artifactbuild.InstallKeystoreScript() + "\n" + build

	buildRepos := ""
	if len(recipe.Repositories) > 0 {
		for c, i := range recipe.Repositories {
			if c == 0 {
				buildRepos = "-" + i
			} else {
				buildRepos = buildRepos + "," + i
			}
		}
	}
	build = strings.ReplaceAll(build, "{{BUILD}}", buildToolSection)
	build = strings.ReplaceAll(build, "{{INSTALL_PACKAGE_SCRIPT}}", install)
	build = strings.ReplaceAll(build, "{{PRE_BUILD_SCRIPT}}", recipe.PreBuildScript)
	build = strings.ReplaceAll(build, "{{POST_BUILD_SCRIPT}}", recipe.PostBuildScript)
	cacheUrl := "https://jvm-build-workspace-artifact-cache-tls." + jbsConfig.Namespace + ".svc.cluster.local/v2/cache/rebuild"
	if jbsConfig.Spec.CacheSettings.DisableTLS {
		cacheUrl = "http://jvm-build-workspace-artifact-cache." + jbsConfig.Namespace + ".svc.cluster.local/v2/cache/rebuild"
	}
	cacheUrl = cacheUrl + buildRepos + "/" + strconv.FormatInt(commitTime, 10)

	//we generate a docker file that can be used to reproduce this build
	//this is for diagnostic purposes, if you have a failing build it can be really hard to figure out how to fix it without this
	log.Info(fmt.Sprintf("Generating dockerfile with recipe build image %#v", recipe.Image))
	preprocessorScript := "#!/bin/sh\n/var/workdir/software/system-java/bin/java -jar /var/workdir/software/build-request-processor/quarkus-run.jar " + doSubstitution(strings.Join(preprocessorArgs, " "), paramValues, commitTime, buildRepos) + "\n"
	buildScript := doSubstitution(build, paramValues, commitTime, buildRepos)
	envVars := extractEnvVar(toolEnv)
	cmdArgs := extractArrayParam(PipelineParamGoals, paramValues)
	konfluxScript := "#!/bin/sh\n" + envVars + "\nset -- \"$@\" " + cmdArgs + "\n\n" + buildScript

	fmt.Printf("### Using cacheUrl %#v paramValues %#v, commitTime %#v, buildRepos %#v\n", cacheUrl, paramValues, commitTime, buildRepos)

	// Diagnostic Containerfile
	// TODO: Looks like diagnostic files won't work with UBI7 anymore. This needs to be followed up on; potentially
	//		 we could just disable the cache for this scenario?
	df := "FROM " + buildRequestProcessorImage + " AS build-request-processor" +
		"\nFROM " + strings.ReplaceAll(buildRequestProcessorImage, "hacbs-jvm-build-request-processor", "hacbs-jvm-cache") + " AS cache" +
		"\nFROM " + recipe.Image +
		"\nUSER 0" +
		"\nWORKDIR /var/workdir" +
		"\nENV CACHE_URL=" + doSubstitution("$(params."+PipelineParamCacheUrl+")", paramValues, commitTime, buildRepos) +
		"\nRUN mkdir -p /var/workdir/software/settings /original-content/marker" +
		"\nCOPY --from=build-request-processor /deployments/ /var/workdir/software/build-request-processor" +
		// Copying JDK17 for the cache.
		// TODO: Could we determine if we are using UBI8 and avoid this?
		"\nCOPY --from=build-request-processor /lib/jvm/jre-17 /var/workdir/software/system-java" +
		"\nCOPY --from=build-request-processor /etc/java/java-17-openjdk /etc/java/java-17-openjdk" +
		"\nCOPY --from=cache /deployments/ /var/workdir/software/cache" +
		// Use git script rather than the preBuildImages as they are OCI archives and can't be used with docker/podman.
		// TODO: ### Is this gitscript using the correct SHA? The Konflux one is but this is using pre preprocessor changes.
		"\nRUN " + doSubstitution(gitScript, paramValues, commitTime, buildRepos) +
		"\nRUN echo " + base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\n/var/workdir/software/system-java/bin/java -Dbuild-policy.default.store-list=rebuilt,central,jboss,redhat -Dkube.disabled=true -Dquarkus.kubernetes-client.trust-certs=true -jar /var/workdir/software/cache/quarkus-run.jar >/var/workdir/cache.log &"+
		"\nwhile ! cat /var/workdir/cache.log | grep 'Listening on:'; do\n        echo \"Waiting for Cache to start\"\n        sleep 1\ndone \n")) + " | base64 -d >/var/workdir/start-cache.sh" +
		"\nRUN echo " + base64.StdEncoding.EncodeToString([]byte(preprocessorScript)) + " | base64 -d >/var/workdir/preprocessor.sh" +
		"\nRUN echo " + base64.StdEncoding.EncodeToString([]byte(buildScript)) + " | base64 -d >/var/workdir/build.sh" +
		"\nRUN echo " + base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\n/var/workdir/preprocessor.sh\n"+envVars+"\n/var/workdir/build.sh "+cmdArgs+"\n")) + " | base64 -d >/var/workdir/run-full-build.sh" +
		"\nRUN echo " + base64.StdEncoding.EncodeToString([]byte(dockerfileEntryScript)) + " | base64 -d >/var/workdir/entry-script.sh" +
		"\nRUN chmod +x /var/workdir/*.sh" +
		"\nCMD [ \"/bin/bash\", \"/var/workdir/entry-script.sh\" ]"

	fmt.Printf("### Using recipe %#v with tool %#v and buildRequestImage %#v \n", recipe.Image, tool, buildRequestProcessorImage)

	// Konflux Containerfile
	kf := "FROM " + recipe.Image +
		"\nUSER 0" +
		"\nWORKDIR /var/workdir" +
		"\nRUN mkdir -p /var/workdir/software/settings /original-content/marker" +
		"\nARG CACHE_URL=\"\"" +
		"\nENV CACHE_URL=$CACHE_URL" +
		// TODO ### HACK : How to use SSL to avoid certificate problem with buildah task?
		//"\nENV JBS_DISABLE_CACHE=true" +
		"\nCOPY .jbs/run-build.sh /var/workdir" +
		"\nCOPY . /var/workdir/workspace/source/" +
		"\nRUN /var/workdir/run-build.sh"
	// TODO: This is a bit of a hack but as Ant doesn't deploy and the previous implementation relied upon using the
	//     BuildRequestProcessorImage we need to modify the Containerfile. In future the ant-build.sh should probably
	//     encapsulate this.
	if tool == "ant" {
		kf = kf +
			"\nFROM " + buildRequestProcessorImage + " AS build-request-processor" +
			"\nUSER 0" +
			"\nWORKDIR /var/workdir" +
			"\nCOPY --from=0 /var/workdir/ /var/workdir/" +
			// Don't think we need to mess with keystore as copy-artifacts is simply calling copy commands.
			"\nRUN /opt/jboss/container/java/run/run-java.sh " + doSubstitution(strings.Join(copyArtifactsArgs, " "), []tektonpipeline.Param{}, commitTime, buildRepos) +
			"\nFROM scratch" +
			"\nCOPY --from=1 /var/workdir/workspace/artifacts /"
	} else {
		kf = kf +
			"\nFROM scratch" +
			"\nCOPY --from=0 /var/workdir/workspace/artifacts /"
	}

	pullPolicy := pullPolicy(buildRequestProcessorImage)
	limits, err := memoryLimits(jbsConfig, additionalMemory)
	if err != nil {
		return nil, "", "", "", err
	}

	createBuildScript := createBuildScript(build)
	pipelineParams := []tektonpipeline.ParamSpec{
		{Name: PipelineBuildId, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamScmUrl, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamScmTag, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamScmHash, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamChainsGitUrl, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamChainsGitCommit, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamGoals, Type: tektonpipeline.ParamTypeArray},
		{Name: PipelineParamJavaVersion, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamToolVersion, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamPath, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamEnforceVersion, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamProjectVersion, Type: tektonpipeline.ParamTypeString},
		{Name: PipelineParamCacheUrl, Type: tektonpipeline.ParamTypeString, Default: &tektonpipeline.ResultValue{Type: tektonpipeline.ParamTypeString, StringVal: cacheUrl}},
	}
	secretVariables := secretVariables(jbsConfig)

	runAfter := make([]string, 0)
	var runAfterBuild []string

	preBuildImage := existingImages[recipe.Image+"-"+recipe.Tool]
	preBuildImageRequired := preBuildImage == ""
	if preBuildImageRequired {
		preBuildImage = "$(tasks." + PreBuildTaskName + ".results." + PipelineResultPreBuildImageDigest + ")"
		runAfter = []string{PreBuildTaskName}
	}
	runAfterBuild = append(runAfter, BuildTaskName)

	ps := &tektonpipeline.PipelineSpec{
		Workspaces: []tektonpipeline.PipelineWorkspaceDeclaration{{Name: WorkspaceBuildSettings}, {Name: WorkspaceSource}, {Name: WorkspaceTls}},
	}

	if preBuildImageRequired {
		buildSetup := tektonpipeline.TaskSpec{
			Workspaces: []tektonpipeline.WorkspaceDeclaration{{Name: WorkspaceBuildSettings}, {Name: WorkspaceSource, MountPath: WorkspaceMount}, {Name: WorkspaceTls}},
			Params:     pipelineParams,
			Results: []tektonpipeline.TaskResult{
				{Name: PipelineResultPreBuildImageDigest, Type: tektonpipeline.ResultsTypeString},
				{Name: PipelineResultGitArchive, Type: tektonpipeline.ResultsTypeString},
			},
			Steps: []tektonpipeline.Step{
				{
					Name:            "git-clone-and-settings",
					Image:           recipe.Image,
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					ComputeResources: v1.ResourceRequirements{
						Requests: v1.ResourceList{"memory": limits.defaultRequestMemory, "cpu": limits.defaultRequestCPU},
						Limits:   v1.ResourceList{"memory": limits.defaultRequestMemory, "cpu": limits.defaultLimitCPU},
					},
					Script: gitScript + "\n" + createBuildScript,
					Env: []v1.EnvVar{
						{Name: PipelineParamCacheUrl, Value: "$(params." + PipelineParamCacheUrl + ")"},
						{Name: "GIT_TOKEN", ValueFrom: &v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: v1alpha1.GitSecretName}, Key: v1alpha1.GitSecretTokenKey, Optional: &trueBool}}},
					},
				},
				{
					Name:            "preprocessor",
					Image:           buildRequestProcessorImage,
					ImagePullPolicy: pullPolicy,
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					Env: []v1.EnvVar{
						{Name: PipelineParamCacheUrl, Value: "$(params." + PipelineParamCacheUrl + ")"},
					},
					ComputeResources: v1.ResourceRequirements{
						Requests: v1.ResourceList{"memory": limits.defaultRequestMemory, "cpu": limits.defaultRequestCPU},
						Limits:   v1.ResourceList{"memory": limits.defaultRequestMemory, "cpu": limits.defaultLimitCPU},
					},
					Script: artifactbuild.InstallKeystoreIntoBuildRequestProcessor(preprocessorArgs),
				},
				{
					Name:            "create-pre-build-source",
					Image:           buildRequestProcessorImage,
					ImagePullPolicy: pullPolicy,
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					Env:             secretVariables,
					ComputeResources: v1.ResourceRequirements{
						Requests: v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultRequestCPU},
						Limits:   v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultLimitCPU},
					},
					Script: createKonfluxScripts(kf, konfluxScript) + "\n" + artifactbuild.InstallKeystoreIntoBuildRequestProcessor(konfluxArgs),
				},
				{
					Name:            "create-pre-build-image",
					Image:           strings.TrimSpace(strings.Split(buildTrustedArtifacts, "FROM")[1]),
					ImagePullPolicy: v1.PullIfNotPresent,
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					Env:             secretVariables,
					ComputeResources: v1.ResourceRequirements{
						Requests: v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultRequestCPU},
						Limits:   v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultLimitCPU},
					},
					Script: preBuildImageArgs,
				},
			},
		}
		pipelineTask := []tektonpipeline.PipelineTask{{
			Name: PreBuildTaskName,
			TaskSpec: &tektonpipeline.EmbeddedTask{
				TaskSpec: buildSetup,
			},
			Params: []tektonpipeline.Param{}, Workspaces: []tektonpipeline.WorkspacePipelineTaskBinding{
				{Name: WorkspaceBuildSettings, Workspace: WorkspaceBuildSettings},
				{Name: WorkspaceSource, Workspace: WorkspaceSource},
				{Name: WorkspaceTls, Workspace: WorkspaceTls},
			},
		}}
		ps.Tasks = append(pipelineTask, ps.Tasks...)

		for _, i := range buildSetup.Results {
			ps.Results = append(ps.Results, tektonpipeline.PipelineResult{Name: i.Name, Description: i.Description, Value: tektonpipeline.ResultValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(tasks." + PreBuildTaskName + ".results." + i.Name + ")"}})
		}
	}

	if jbsConfig.Spec.ContainerBuilds {
		// Note - its also possible to refer to a remote pipeline ref as well as a task.
		resolver := tektonpipeline.ResolverRef{
			// We can use either a http or git resolver. Using http as avoids cloning an entire repository.
			Resolver: "http",
			Params: []tektonpipeline.Param{
				{
					Name: "url",
					Value: tektonpipeline.ParamValue{
						Type:      tektonpipeline.ParamTypeString,
						StringVal: v1alpha1.KonfluxBuildDefinitions,
					},
				},
			},
		}

		ps.Tasks = append([]tektonpipeline.PipelineTask{
			{
				Name:     BuildTaskName,
				RunAfter: runAfter,
				TaskRef: &tektonpipeline.TaskRef{
					// Can't specify name and resolver as they clash.
					ResolverRef: resolver,
				},
				Timeout: &v12.Duration{Duration: time.Hour * v1alpha1.DefaultTimeout},
				Params: []tektonpipeline.Param{
					{
						Name: "DOCKERFILE",
						Value: tektonpipeline.ParamValue{
							Type:      tektonpipeline.ParamTypeString,
							StringVal: ".jbs/Containerfile"},
					},
					{
						Name: "IMAGE",
						Value: tektonpipeline.ParamValue{
							Type:      tektonpipeline.ParamTypeString,
							StringVal: registryArgsWithDefaults(jbsConfig, buildId)},
					},
					{
						Name: "SOURCE_ARTIFACT",
						Value: tektonpipeline.ParamValue{
							Type:      tektonpipeline.ParamTypeString,
							StringVal: preBuildImage,
						},
					},
					{
						Name: "ORAS_OPTIONS",
						Value: tektonpipeline.ParamValue{
							Type:      tektonpipeline.ParamTypeString,
							StringVal: orasOptions,
						},
					},
					{
						Name: "TLSVERIFY",
						Value: tektonpipeline.ParamValue{
							Type:      tektonpipeline.ParamTypeString,
							StringVal: tlsVerify,
						},
					},
				},

				// TODO: ### How to pass build-settings/tls information to buildah task?
				//       Note - buildah-oci-ta task has no defined workspace
				//Workspaces: []tektonpipeline.WorkspacePipelineTaskBinding{
				//	//{Name: WorkspaceBuildSettings, Workspace: WorkspaceBuildSettings},
				//	{Name: WorkspaceSource, Workspace: WorkspaceSource},
				//	//{Name: WorkspaceTls, Workspace: WorkspaceTls},
				//},
			}}, ps.Tasks...)

		// Results for https://github.com/konflux-ci/build-definitions/tree/main/task/buildah-oci-ta/0.2
		// IMAGE_DIGEST
		// IMAGE_URL
		ps.Results = append(ps.Results, tektonpipeline.PipelineResult{Name: PipelineResultImage, Value: tektonpipeline.ResultValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(tasks." + BuildTaskName + ".results." + PipelineResultImage + ")"}})
		ps.Results = append(ps.Results, tektonpipeline.PipelineResult{Name: PipelineResultImageDigest, Value: tektonpipeline.ResultValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(tasks." + BuildTaskName + ".results." + PipelineResultImageDigest + ")"}})
	} else {
		buildTask := tektonpipeline.TaskSpec{
			Workspaces: []tektonpipeline.WorkspaceDeclaration{{Name: WorkspaceBuildSettings}, {Name: WorkspaceSource, MountPath: WorkspaceMount}, {Name: WorkspaceTls}},
			Params:     append(pipelineParams, tektonpipeline.ParamSpec{Name: PipelineResultPreBuildImageDigest, Type: tektonpipeline.ParamTypeString}),
			Results: []tektonpipeline.TaskResult{
				{Name: PipelineResultImage},
				{Name: PipelineResultImageDigest},
			},
			Steps: []tektonpipeline.Step{
				{
					Name:            "restore-pre-build-source",
					Image:           strings.TrimSpace(strings.Split(buildTrustedArtifacts, "FROM")[1]),
					ImagePullPolicy: v1.PullIfNotPresent,
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					Env:             secretVariables,
					Script: fmt.Sprintf(`echo "Restoring source to workspace : $(workspaces.source.path)"
export ORAS_OPTIONS="%s"
use-archive $(params.%s)=$(workspaces.source.path)/source
mv $(workspaces.source.path)/source/.jbs/build.sh $(workspaces.source.path)`, orasOptions, PipelineResultPreBuildImageDigest),
				},
				{
					Timeout:         &v12.Duration{Duration: time.Hour * v1alpha1.DefaultTimeout},
					Name:            "build",
					Image:           recipe.Image,
					ImagePullPolicy: pullPolicy,
					WorkingDir:      "$(workspaces." + WorkspaceSource + ".path)/source",
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					Env:             append(toolEnv, v1.EnvVar{Name: PipelineParamCacheUrl, Value: "$(params." + PipelineParamCacheUrl + ")"}),
					ComputeResources: v1.ResourceRequirements{
						Requests: v1.ResourceList{"memory": limits.buildRequestMemory, "cpu": limits.buildRequestCPU},
						Limits:   v1.ResourceList{"memory": limits.buildRequestMemory, "cpu": limits.buildLimitCPU},
					},
					Args:   []string{"$(params.GOALS[*])"},
					Script: "$(workspaces." + WorkspaceSource + ".path)/build.sh \"$@\"",
				},
				{
					Name:            "deploy-ant-artifacts",
					Image:           buildRequestProcessorImage,
					ImagePullPolicy: pullPolicy,
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					Env:             secretVariables,
					ComputeResources: v1.ResourceRequirements{
						Requests: v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultRequestCPU},
						Limits:   v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultLimitCPU},
					},
					Script: artifactbuild.InstallKeystoreIntoBuildRequestProcessor(copyArtifactsArgs),
				},
				// Store post-build artifacts here using oras to match container build
				{
					Name:            "store-post-build-artifacts",
					Image:           strings.TrimSpace(strings.Split(buildTrustedArtifacts, "FROM")[1]),
					ImagePullPolicy: v1.PullIfNotPresent,
					SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
					Env:             secretVariables,
					Script: fmt.Sprintf(`echo "Creating post-build-image archive"
export ORAS_OPTIONS="%s --image-spec=v1.0 --artifact-type application/vnd.oci.image.config.v1+json --no-tty --format=json"
IMGURL=%s
create-archive --store $IMGURL /tmp/artifacts=$(workspaces.source.path)/artifacts | tee /tmp/oras-create.json
IMGDIGEST=$(cat /tmp/oras-create.json | grep -Ev '(Prepared artifact|Artifacts created)' | jq -r '.digest')
echo "Storing IMGURL $IMGURL and IMGDIGEST $IMGDIGEST"
echo -n "$IMGURL" >> $(results.%s.path)
echo -n "$IMGDIGEST" >> $(results.%s.path)
`, orasOptions, registryArgsWithDefaults(jbsConfig, buildId+"-artifacts"), PipelineResultImage, PipelineResultImageDigest),
				},
			}}

		pipelineTask := []tektonpipeline.PipelineTask{{
			Name:     BuildTaskName,
			RunAfter: runAfter,
			TaskSpec: &tektonpipeline.EmbeddedTask{
				TaskSpec: buildTask,
			},
			Timeout: &v12.Duration{Duration: time.Hour * v1alpha1.DefaultTimeout},
			Params: []tektonpipeline.Param{
				{
					Name:  PipelineResultPreBuildImageDigest,
					Value: tektonpipeline.ParamValue{Type: tektonpipeline.ParamTypeString, StringVal: preBuildImage},
				},
			},
			Workspaces: []tektonpipeline.WorkspacePipelineTaskBinding{
				{Name: WorkspaceBuildSettings, Workspace: WorkspaceBuildSettings},
				{Name: WorkspaceSource, Workspace: WorkspaceSource},
				{Name: WorkspaceTls, Workspace: WorkspaceTls},
			},
		}}
		ps.Tasks = append(pipelineTask, ps.Tasks...)

		for _, i := range buildTask.Results {
			ps.Results = append(ps.Results, tektonpipeline.PipelineResult{Name: i.Name, Description: i.Description, Value: tektonpipeline.ResultValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(tasks." + BuildTaskName + ".results." + i.Name + ")"}})
		}
	}

	postBuildTask := tektonpipeline.TaskSpec{
		Workspaces: []tektonpipeline.WorkspaceDeclaration{{Name: WorkspaceBuildSettings}, {Name: WorkspaceSource, MountPath: WorkspaceMount}, {Name: WorkspaceTls}},
		Params:     append(pipelineParams, tektonpipeline.ParamSpec{Name: PipelineResultPreBuildImageDigest, Type: tektonpipeline.ParamTypeString}),
		Results: []tektonpipeline.TaskResult{
			{Name: PipelineResultContaminants},
			{Name: PipelineResultDeployedResources},
			{Name: PipelineResultPassedVerification},
			{Name: PipelineResultVerificationResult},
		},
		Steps: []tektonpipeline.Step{
			{
				Name:            "restore-post-build-artifacts",
				Image:           strings.TrimSpace(strings.Split(buildTrustedArtifacts, "FROM")[1]),
				ImagePullPolicy: v1.PullIfNotPresent,
				SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
				Env:             secretVariables,
				// While the manifest digest is available we need the manifest of the layer within the archive hence
				// using 'oras manifest fetch' to extract the correct layer.
				Script: fmt.Sprintf(`echo "Restoring artifacts to workspace : $(workspaces.source.path)"
export ORAS_OPTIONS="%s"
URL=%s
DIGEST=$(tasks.%s.results.IMAGE_DIGEST)
AARCHIVE=$(oras manifest fetch $ORAS_OPTIONS $URL@$DIGEST | jq --raw-output '.layers[0].digest')
echo "URL $URL DIGEST $DIGEST AARCHIVE $AARCHIVE"
use-archive oci:$URL@$AARCHIVE=$(workspaces.source.path)/artifacts`, orasOptions, registryArgsWithDefaults(jbsConfig, ""), BuildTaskName),
			},
			{
				Name:            "verify-and-check-for-contaminates",
				Image:           buildRequestProcessorImage,
				ImagePullPolicy: pullPolicy,
				SecurityContext: &v1.SecurityContext{RunAsUser: &zero},
				Env:             secretVariables,
				ComputeResources: v1.ResourceRequirements{
					Requests: v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultRequestCPU},
					Limits:   v1.ResourceList{"memory": limits.defaultBuildRequestMemory, "cpu": limits.defaultLimitCPU},
				},
				Script: artifactbuild.InstallKeystoreIntoBuildRequestProcessor(verifyBuiltArtifactsArgs, deployArgs),
			},
		},
	}
	pipelineTask := []tektonpipeline.PipelineTask{{
		Name:     PostBuildTaskName,
		RunAfter: runAfterBuild,
		TaskSpec: &tektonpipeline.EmbeddedTask{
			TaskSpec: postBuildTask,
		},
		Timeout: &v12.Duration{Duration: time.Hour * v1alpha1.DefaultTimeout},
		Params:  []tektonpipeline.Param{{Name: PipelineResultPreBuildImageDigest, Value: tektonpipeline.ParamValue{Type: tektonpipeline.ParamTypeString, StringVal: preBuildImage}}},
		Workspaces: []tektonpipeline.WorkspacePipelineTaskBinding{
			{Name: WorkspaceBuildSettings, Workspace: WorkspaceBuildSettings},
			{Name: WorkspaceSource, Workspace: WorkspaceSource},
			{Name: WorkspaceTls, Workspace: WorkspaceTls},
		},
	}}
	ps.Tasks = append(pipelineTask, ps.Tasks...)

	for _, i := range postBuildTask.Results {
		ps.Results = append(ps.Results, tektonpipeline.PipelineResult{Name: i.Name, Description: i.Description, Value: tektonpipeline.ResultValue{Type: tektonpipeline.ParamTypeString, StringVal: "$(tasks." + PostBuildTaskName + ".results." + i.Name + ")"}})
	}

	for _, i := range pipelineParams {
		ps.Params = append(ps.Params, tektonpipeline.ParamSpec{Name: i.Name, Description: i.Description, Default: i.Default, Type: i.Type})
		var value tektonpipeline.ResultValue
		if i.Type == tektonpipeline.ParamTypeString {
			value = tektonpipeline.ResultValue{Type: i.Type, StringVal: "$(params." + i.Name + ")"}
		} else {
			value = tektonpipeline.ResultValue{Type: i.Type, ArrayVal: []string{"$(params." + i.Name + "[*])"}}
		}
		ps.Tasks[0].Params = append(ps.Tasks[0].Params, tektonpipeline.Param{
			Name:  i.Name,
			Value: value})
		index := 0
		if preBuildImageRequired {
			index += 1
			ps.Tasks[index].Params = append(ps.Tasks[index].Params, tektonpipeline.Param{
				Name:  i.Name,
				Value: value})
		}
		index += 1
		ps.Tasks[index].Params = append(ps.Tasks[index].Params, tektonpipeline.Param{
			Name:  i.Name,
			Value: value})
	}

	return ps, df, kf, konfluxScript, nil
}

func secretVariables(jbsConfig *v1alpha1.JBSConfig) []v1.EnvVar {
	trueBool := true
	secretVariables := make([]v1.EnvVar, 0)
	if jbsConfig.ImageRegistry().SecretName != "" {
		// Builds or tooling mostly use the .docker/config.json directly which is updated via Tekton/Kubernetes secrets. But the
		// Java code may require the token as well.
		secretVariables = []v1.EnvVar{
			{Name: "REGISTRY_TOKEN", ValueFrom: &v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: jbsConfig.ImageRegistry().SecretName}, Key: v1alpha1.ImageSecretTokenKey, Optional: &trueBool}}},
		}
	}
	if jbsConfig.Spec.MavenDeployment.Repository != "" {
		secretVariables = append(secretVariables, v1.EnvVar{Name: "MAVEN_PASSWORD", ValueFrom: &v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: v1alpha1.MavenSecretName}, Key: v1alpha1.MavenSecretKey, Optional: &trueBool}}})

		secretVariables = append(secretVariables, v1.EnvVar{Name: "AWS_ACCESS_KEY_ID", ValueFrom: &v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: v1alpha1.AWSSecretName}, Key: v1alpha1.AWSAccessID, Optional: &trueBool}}})
		secretVariables = append(secretVariables, v1.EnvVar{Name: "AWS_SECRET_ACCESS_KEY", ValueFrom: &v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: v1alpha1.AWSSecretName}, Key: v1alpha1.AWSSecretKey, Optional: &trueBool}}})
		secretVariables = append(secretVariables, v1.EnvVar{Name: "AWS_PROFILE", ValueFrom: &v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: v1alpha1.AWSSecretName}, Key: v1alpha1.AWSProfile, Optional: &trueBool}}})
	}
	if jbsConfig.Spec.GitSourceArchive.Identity != "" {
		secretVariables = append(secretVariables, v1.EnvVar{Name: "GIT_DEPLOY_TOKEN", ValueFrom: &v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{LocalObjectReference: v1.LocalObjectReference{Name: v1alpha1.GitRepoSecretName}, Key: v1alpha1.GitRepoSecretKey, Optional: &trueBool}}})
	}
	return secretVariables
}

func createBuildScript(build string) string {
	ret := "tee $(workspaces." + WorkspaceSource + ".path)/build.sh <<'RHTAPEOF'\n"
	ret += build
	ret += "\nRHTAPEOF\n"
	ret += "chmod +x $(workspaces." + WorkspaceSource + ".path)/build.sh\n"
	return ret
}

func createKonfluxScripts(containerfile string, konfluxScript string) string {
	ret := "mkdir -p $(workspaces." + WorkspaceSource + ".path)/source/.jbs\n"
	ret += "tee $(workspaces." + WorkspaceSource + ".path)/source/.jbs/Containerfile <<'RHTAPEOF'\n"
	ret += containerfile
	ret += "\nRHTAPEOF\n"
	ret += "tee $(workspaces." + WorkspaceSource + ".path)/source/.jbs/run-build.sh <<'RHTAPEOF'\n"
	ret += konfluxScript
	ret += "\nRHTAPEOF\n"
	ret += "chmod +x $(workspaces." + WorkspaceSource + ".path)/source/.jbs/run-build.sh\n"
	return ret
}

func pullPolicy(buildRequestProcessorImage string) v1.PullPolicy {
	pullPolicy := v1.PullIfNotPresent
	if strings.HasSuffix(buildRequestProcessorImage, ":dev") {
		pullPolicy = v1.PullAlways
	}
	return pullPolicy
}

type memLimits struct {
	defaultRequestMemory, defaultBuildRequestMemory, defaultRequestCPU, defaultLimitCPU, buildRequestCPU, buildLimitCPU, buildRequestMemory resource.Quantity
}

func memoryLimits(jbsConfig *v1alpha1.JBSConfig, additionalMemory int) (*memLimits, error) {
	limits := memLimits{}
	var err error
	limits.defaultRequestMemory, err = resource.ParseQuantity(settingOrDefault(jbsConfig.Spec.BuildSettings.TaskRequestMemory, "512Mi"))
	if err != nil {
		return nil, err
	}
	limits.defaultBuildRequestMemory, err = resource.ParseQuantity(settingOrDefault(jbsConfig.Spec.BuildSettings.BuildRequestMemory, "1024Mi"))
	if err != nil {
		return nil, err
	}
	limits.defaultRequestCPU, err = resource.ParseQuantity(settingOrDefault(jbsConfig.Spec.BuildSettings.TaskRequestCPU, "10m"))
	if err != nil {
		return nil, err
	}
	limits.defaultLimitCPU, err = resource.ParseQuantity(settingOrDefault(jbsConfig.Spec.BuildSettings.TaskLimitCPU, "300m"))
	if err != nil {
		return nil, err
	}
	limits.buildRequestCPU, err = resource.ParseQuantity(settingOrDefault(jbsConfig.Spec.BuildSettings.BuildRequestCPU, "300m"))
	if err != nil {
		return nil, err
	}
	limits.buildLimitCPU, err = resource.ParseQuantity(settingOrDefault(jbsConfig.Spec.BuildSettings.BuildLimitCPU, "2"))
	if err != nil {
		return nil, err
	}

	limits.buildRequestMemory = limits.defaultBuildRequestMemory
	if additionalMemory > 0 {
		additional := resource.MustParse(fmt.Sprintf("%dMi", additionalMemory))
		limits.buildRequestMemory.Add(additional)
		limits.defaultRequestMemory.Add(additional)
	}
	return &limits, nil
}

func additionalPackages(recipe *v1alpha1.BuildRecipe) string {
	install := ""
	for count, i := range recipe.AdditionalDownloads {
		if i.FileType == "tar" {
			if i.BinaryPath == "" {
				install = "echo 'Binary path not specified for package " + i.Uri + "'; exit 1"
			}

		} else if i.FileType == "executable" {
			if i.FileName == "" {
				install = "echo 'File name not specified for package " + i.Uri + "'; exit 1"
			}
		} else if i.FileType == "rpm" {
			if i.PackageName == "" {
				install = "echo 'Package name not specified for rpm type'; exit 1"
			}
		} else {
			//unknown
			//we still run the pipeline so there is logs
			install = "echo 'Unknown file type " + i.FileType + " for package " + i.Uri + "'; exit 1"
			break
		}
		template := packageTemplate
		fileName := i.FileName
		if fileName == "" {
			fileName = "package-" + strconv.Itoa(count)
		}
		template = strings.ReplaceAll(template, "{URI}", i.Uri)
		template = strings.ReplaceAll(template, "{FILENAME}", fileName)
		template = strings.ReplaceAll(template, "{SHA256}", i.Sha256)
		template = strings.ReplaceAll(template, "{TYPE}", i.FileType)
		template = strings.ReplaceAll(template, "{BINARY_PATH}", i.BinaryPath)
		template = strings.ReplaceAll(template, "{PACKAGE_NAME}", i.PackageName)
		install = install + template
	}
	return install
}

func gitScript(db *v1alpha1.DependencyBuild, recipe *v1alpha1.BuildRecipe) string {
	gitArgs := "echo \"Cloning $(params." + PipelineParamScmUrl + ") and resetting to $(params." + PipelineParamScmHash + ")\" && "
	if db.Spec.ScmInfo.Private {
		gitArgs = gitArgs + "echo \"$GIT_TOKEN\" > $HOME/.git-credentials && chmod 400 $HOME/.git-credentials && "
		gitArgs = gitArgs + "echo '[credential]\n        helper=store\n' > $HOME/.gitconfig && "
	}
	gitArgs = gitArgs + "git clone $(params." + PipelineParamScmUrl + ") $(workspaces." + WorkspaceSource + ".path)/source && cd $(workspaces." + WorkspaceSource + ".path)/source && git reset --hard $(params." + PipelineParamScmHash + ")"

	if !recipe.DisableSubmodules {
		gitArgs = gitArgs + " && git submodule init && git submodule update --recursive"
	}
	return gitArgs
}

func pipelineBuildCommands(imageId string, db *v1alpha1.DependencyBuild, jbsConfig *v1alpha1.JBSConfig, buildId string) (string, []string, []string, []string) {

	orasOptions := ""
	if jbsConfig.Annotations != nil && jbsConfig.Annotations[jbsconfig.TestRegistry] == "true" {
		orasOptions = "--insecure --plain-http"
	}

	preBuildImageTag := imageId + "-pre-build-image"
	// The build-trusted-artifacts container doesn't handle REGISTRY_TOKEN but the actual .docker/config.json. Was using
	// AUTHFILE to override but now switched to adding the image secret to the pipeline.
	// Setting ORAS_OPTIONS to ensure the archive is compatible with jib (for OCIRepositoryClient).
	preBuildImageArgs := fmt.Sprintf(`echo "Creating pre-build-image archive"
export ORAS_OPTIONS="%s --image-spec=v1.0 --artifact-type application/vnd.oci.image.config.v1+json"
cp $(workspaces.source.path)/build.sh $(workspaces.source.path)/source/.jbs
create-archive --store %s $(results.%s.path)=$(workspaces.source.path)/source
`, orasOptions, registryArgsWithDefaults(jbsConfig, preBuildImageTag), PipelineResultPreBuildImageDigest)

	copyArtifactsArgs := []string{
		"copy-artifacts",
		"--source-path=$(workspaces.source.path)/source",
		"--deploy-path=$(workspaces.source.path)/artifacts",
	}

	deployArgs := []string{
		"verify",
		"--path=$(workspaces.source.path)/artifacts",
		"--logs-path=$(workspaces.source.path)/logs",
		"--task-run-name=$(context.taskRun.name)",
		"--build-id=" + buildId,
		"--scm-uri=" + db.Spec.ScmInfo.SCMURL,
		"--scm-commit=" + db.Spec.ScmInfo.CommitHash,
	}

	konfluxArgs := []string{
		"deploy-pre-build-source",
		"--source-path=$(workspaces.source.path)/source",
		"--task-run-name=$(context.taskRun.name)",
		"--scm-uri=" + db.Spec.ScmInfo.SCMURL,
		"--scm-commit=" + db.Spec.ScmInfo.CommitHash,
	}
	konfluxArgs = append(konfluxArgs, gitArgs(jbsConfig, db)...)
	konfluxArgs = append(konfluxArgs, "--image-id="+imageId)

	return preBuildImageArgs, copyArtifactsArgs, deployArgs, konfluxArgs
}

// This effectively duplicates the defaults from DeployPreBuildImageCommand.java
func registryArgsWithDefaults(jbsConfig *v1alpha1.JBSConfig, preBuildImageTag string) string {

	imageRegistry := jbsConfig.ImageRegistry()
	var registryArgs strings.Builder
	if imageRegistry.Host != "" {
		registryArgs.WriteString(imageRegistry.Host)
	} else {
		registryArgs.WriteString("quay.io")
	}
	if imageRegistry.Port != "" && imageRegistry.Port != "443" {
		registryArgs.WriteString(":")
		registryArgs.WriteString(imageRegistry.Port)
	}
	// 'else' :
	// No need to pass a default port (of 443) and its not supported by select-oci-auth.sh according
	// to the tests in https://github.com/konflux-ci/build-trusted-artifacts/pull/103
	registryArgs.WriteString("/")
	if imageRegistry.Owner != "" {
		registryArgs.WriteString(imageRegistry.Owner)
		registryArgs.WriteString("/")
	}
	if imageRegistry.Repository != "" {
		registryArgs.WriteString(imageRegistry.Repository)
	} else {
		registryArgs.WriteString("artifact-deployments")
	}
	// If no tag (or digest) is passed in that allows just the host:owner:repo to be reconstructed.
	if preBuildImageTag != "" {
		registryArgs.WriteString(":")
		registryArgs.WriteString(prependTagToImage(preBuildImageTag, imageRegistry.PrependTag))
	}
	return registryArgs.String()
}

func pipelineDeployCommands(jbsConfig *v1alpha1.JBSConfig) []string {

	deployArgs := []string{
		"deploy",
		"--directory=$(workspaces.source.path)/artifacts",
	}

	mavenArgs := make([]string, 0)
	if jbsConfig.Spec.MavenDeployment.Repository != "" {
		mavenArgs = append(mavenArgs, "--mvn-repo="+jbsConfig.Spec.MavenDeployment.Repository)
	}
	if jbsConfig.Spec.MavenDeployment.Username != "" {
		mavenArgs = append(mavenArgs, "--mvn-username="+jbsConfig.Spec.MavenDeployment.Username)
	}
	deployArgs = append(deployArgs, mavenArgs...)

	return deployArgs
}

func gitArgs(jbsConfig *v1alpha1.JBSConfig, db *v1alpha1.DependencyBuild) []string {
	gitArgs := make([]string, 0)
	if jbsConfig.Spec.GitSourceArchive.Identity != "" {
		gitArgs = append(gitArgs, "--git-identity="+jbsConfig.Spec.GitSourceArchive.Identity)
	}
	if jbsConfig.Spec.GitSourceArchive.URL != "" {
		gitArgs = append(gitArgs, "--git-url="+jbsConfig.Spec.GitSourceArchive.URL)
	}
	if jbsConfig.Spec.GitSourceArchive.DisableSSLVerification {
		gitArgs = append(gitArgs, "--git-disable-ssl-verification")
	}
	if db.Annotations[artifactbuild.DependencyScmAnnotation] == "true" {
		gitArgs = append(gitArgs, "--git-reuse-repository")
	}
	return gitArgs
}

// This is similar to ContainerRegistryDeployer.java::createImageName with the same image tag length restriction.
func prependTagToImage(imageId string, prependTag string) string {

	i := strings.LastIndex(imageId, ":")
	var slice, tag string
	if i != -1 {
		slice = imageId[0:i] + ":"
		tag = prependTag + "_" + imageId[i+1:]
	} else {
		slice = ""
		if prependTag != "" {
			tag = prependTag + "_" + imageId
		} else {
			tag = imageId
		}
	}
	if len(tag) > 128 {
		tag = tag[0:128]
	}
	imageId = slice + tag
	return imageId
}

func verifyParameters(jbsConfig *v1alpha1.JBSConfig, recipe *v1alpha1.BuildRecipe) []string {
	verifyBuiltArtifactsArgs := []string{
		"verify-built-artifacts",
		"--repository-url=$(params.CACHE_URL)",
		"--deploy-path=$(workspaces.source.path)/artifacts",
		"--task-run-name=$(context.taskRun.name)",
		"--results-file=$(results." + PipelineResultPassedVerification + ".path)",
	}

	if !jbsConfig.Spec.RequireArtifactVerification {
		verifyBuiltArtifactsArgs = append(verifyBuiltArtifactsArgs, "--report-only")
	}

	if len(recipe.AllowedDifferences) > 0 {
		for _, i := range recipe.AllowedDifferences {
			verifyBuiltArtifactsArgs = append(verifyBuiltArtifactsArgs, "--excludes="+i)
		}
	}
	return verifyBuiltArtifactsArgs
}

func extractArrayParam(key string, paramValues []tektonpipeline.Param) string {
	// Within the recipe parameters its possible variables are used as '-Pversion=$(PROJECT_VERSION)'.
	// However, this only works in the container and not within the diagnostic container files.
	re := regexp.MustCompile("[(]|[)]")
	result := ""
	for _, i := range paramValues {
		if i.Name == key {
			for _, j := range i.Value.ArrayVal {
				result += re.ReplaceAllString(j, "") + " "
			}
		}
	}
	return result
}

func extractEnvVar(envVar []v1.EnvVar) string {
	result := ""
	for _, i := range envVar {
		result += "export " + i.Name + "=" + i.Value + "\n"
	}
	return result
}

func doSubstitution(script string, paramValues []tektonpipeline.Param, commitTime int64, buildRepos string) string {
	for _, i := range paramValues {
		if i.Value.Type == tektonpipeline.ParamTypeString {
			script = strings.ReplaceAll(script, "$(params."+i.Name+")", i.Value.StringVal)
		}
	}
	script = strings.ReplaceAll(script, "$(params.CACHE_URL)", "http://localhost:8080/v2/cache/rebuild"+buildRepos+"/"+strconv.FormatInt(commitTime, 10)+"/")
	script = strings.ReplaceAll(script, "$(workspaces.build-settings.path)", "/var/workdir/software/settings")
	script = strings.ReplaceAll(script, "$(workspaces.source.path)", "/var/workdir/workspace")
	script = strings.ReplaceAll(script, "$(workspaces.tls.path)", "/var/workdir/software/tls/service-ca.crt")
	return script
}

func settingOrDefault(setting, def string) string {
	if len(strings.TrimSpace(setting)) == 0 {
		return def
	}
	return setting
}
